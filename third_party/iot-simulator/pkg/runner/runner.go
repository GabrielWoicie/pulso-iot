// Package runner provides reusable orchestration independent of CLI, HTTP and storage.
package runner

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/woicie/iot-simulator/internal/device"
	"github.com/woicie/iot-simulator/internal/identity"
	"github.com/woicie/iot-simulator/internal/mqtt"
	"github.com/woicie/iot-simulator/internal/telemetry"
	"github.com/woicie/iot-simulator/pkg/config"
	"hash/fnv"
	"strings"
	"sync"
	"time"
)

// Public aliases permit transport injection from external modules.
type Transport = mqtt.Transport
type Receipt = mqtt.Receipt
type Notice = mqtt.Notice
type Factory func(config.Config, config.Device) Transport
type RunRequest struct {
	DeviceIDs   []string       `json:"device_ids,omitempty"`
	Messages    int            `json:"messages"` // zero = continuous; finite CLI requires positive.
	Interval    time.Duration  `json:"interval"` // native Go JSON: nanoseconds; see HTTP mapping in docs.
	QoS         *int           `json:"qos,omitempty"`
	GracePeriod *time.Duration `json:"grace_period,omitempty"`
}
type Event struct {
	RunID     string    `json:"run_id"`
	DeviceID  string    `json:"device_id,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Kind      string    `json:"kind"`
	Message   string    `json:"message"`
	Attempted int       `json:"attempted,omitempty"`
	Published int       `json:"published,omitempty"`
}

// Sink is serialized. Callbacks must return promptly and must not call Run recursively.
type Sink func(Event)
type DeviceResult struct {
	DeviceID              string         `json:"device_id"`
	QoS                   int            `json:"qos"`
	IntervalMS            int64          `json:"interval_ms"`
	Planned               int            `json:"planned"`
	Attempted             int            `json:"attempted"`
	Published             int            `json:"published"`
	BrokerAcknowledged    int            `json:"broker_acknowledged"`
	AckReasonCodes        map[string]int `json:"ack_reason_codes"`
	Failed                int            `json:"failed"`
	CommandsReceived      int            `json:"commands_received"`
	CommandsRejected      int            `json:"commands_rejected"`
	ResponsesPublished    int            `json:"responses_published"`
	ResponsesAcknowledged int            `json:"responses_acknowledged"`
	ResponsesFailed       int            `json:"responses_failed"`
	ConnectionState       string         `json:"connection_state"`
	Connections           int            `json:"connections"`
	Errors                []string       `json:"errors"`
	ErrorsTotal           int            `json:"errors_total"`
	Fatal                 bool           `json:"fatal"`
}
type RunResult struct {
	RunID             string         `json:"run_id"`
	Status            string         `json:"status"`
	StartedAt         time.Time      `json:"started_at"`
	FinishedAt        time.Time      `json:"finished_at"`
	RequestedDevices  int            `json:"requested_devices"`
	MessagesPerDevice int            `json:"messages_per_device"`
	ResponseQoS       int            `json:"response_qos"`
	Reason            string         `json:"reason,omitempty"`
	Devices           []DeviceResult `json:"devices"`
}
type Runner struct {
	cfg     config.Config
	factory Factory
}

func New(c config.Config, f Factory) (*Runner, error) {
	c.Devices = append([]config.Device(nil), c.Devices...)
	if e := c.Normalize(); e != nil {
		return nil, e
	}
	if f == nil {
		f = func(c config.Config, d config.Device) Transport { return mqtt.New(c, d) }
	}
	return &Runner{c, f}, nil
}
func Select(c config.Config, r RunRequest) ([]config.Device, error) {
	if r.Messages < 0 {
		return nil, errors.New("messages não pode ser negativo")
	}
	if r.Interval < 0 {
		return nil, errors.New("interval deve ser positivo")
	}
	if r.QoS != nil && (*r.QoS < 0 || *r.QoS > 2) {
		return nil, errors.New("qos deve estar entre 0 e 2")
	}
	if r.GracePeriod != nil && *r.GracePeriod < 0 {
		return nil, errors.New("grace_period não pode ser negativo")
	}
	wanted := map[string]bool{}
	for _, id := range r.DeviceIDs {
		if wanted[id] {
			return nil, errors.New("seleção contém dispositivo duplicado")
		}
		wanted[id] = true
	}
	// Preserve the full filter while iterating, including after the last match.
	filtered := len(wanted) > 0
	var out []config.Device
	for _, d := range c.Devices {
		if filtered && !wanted[d.ID] {
			continue
		}
		if r.QoS != nil {
			q := *r.QoS
			d.QoS = &q
		}
		if r.Interval > 0 {
			d.Interval = config.Duration(r.Interval)
		}
		out = append(out, d)
		delete(wanted, d.ID)
	}
	if len(wanted) > 0 {
		return nil, errors.New("seleção contém dispositivo desconhecido")
	}
	if len(out) == 0 {
		return nil, errors.New("nenhum dispositivo selecionado")
	}
	return out, nil
}

// Run blocks until all clients close. Cancellation returns the partial final result and ctx.Err().
func (r *Runner) Run(ctx context.Context, req RunRequest, sink Sink) (RunResult, error) {
	devices, e := Select(r.cfg, req)
	if e != nil {
		return RunResult{}, e
	}
	result := RunResult{RunID: identity.New(), Status: "running", StartedAt: time.Now().UTC(), RequestedDevices: len(devices), MessagesPerDevice: req.Messages, ResponseQoS: r.cfg.ResponseQoS, Devices: make([]DeviceResult, len(devices))}
	var eventMu sync.Mutex
	emit := func(id, kind, msg string, a, p int) {
		if sink != nil {
			eventMu.Lock()
			defer eventMu.Unlock()
			sink(Event{result.RunID, id, time.Now().UTC(), kind, msg, a, p})
		}
	}
	emit("", "run_started", "execução iniciada", 0, 0)
	var wg, telemetryWG sync.WaitGroup
	telemetryWG.Add(len(devices))
	allPublished := make(chan struct{})
	go func() { telemetryWG.Wait(); close(allPublished) }()
	for i, d := range devices {
		wg.Add(1)
		go func(i int, d config.Device) {
			defer wg.Done()
			result.Devices[i] = r.runDevice(ctx, req, d, emit, &telemetryWG, allPublished)
		}(i, d)
	}
	wg.Wait()
	result.FinishedAt = time.Now().UTC()
	result.Status = "completed"
	if ctx.Err() != nil {
		result.Status = "cancelled"
		result.Reason = ctx.Err().Error()
		e = ctx.Err()
	} else {
		for _, d := range result.Devices {
			if d.Fatal || d.Failed > 0 || d.ResponsesFailed > 0 {
				result.Status = "failed"
				result.Reason = "um ou mais dispositivos apresentaram falhas"
				e = errors.New(result.Reason)
				break
			}
		}
	}
	emit("", "run_finished", result.Status, 0, 0)
	return result, e
}

type emitter func(string, string, string, int, int)

func (r *Runner) runDevice(ctx context.Context, req RunRequest, d config.Device, emit emitter, telemetryWG *sync.WaitGroup, allPublished <-chan struct{}) (s DeviceResult) {
	s = DeviceResult{DeviceID: d.ID, QoS: *d.QoS, IntervalMS: d.Interval.Value().Milliseconds(), Planned: req.Messages, ConnectionState: "connecting", Errors: []string{}, AckReasonCodes: map[string]int{}}
	var mu sync.Mutex
	clean := func(msg string) string {
		for _, other := range r.cfg.Devices {
			if other.Password != "" {
				msg = strings.ReplaceAll(msg, other.Password, "[REDACTED]")
			}
		}
		if len(msg) > 512 {
			msg = msg[:512]
		}
		return msg
	}
	addError := func(msg string) {
		msg = clean(msg)
		mu.Lock()
		s.ErrorsTotal++
		if len(s.Errors) < 100 {
			s.Errors = append(s.Errors, msg)
		}
		mu.Unlock()
		emit(d.ID, "error", msg, 0, 0)
	}
	dctx, cancel := context.WithCancel(ctx)
	marked := false
	markDone := func() {
		if !marked {
			marked = true
			telemetryWG.Done()
		}
	}
	transport := r.factory(r.cfg, d)
	var commandWG sync.WaitGroup
	commands := make(chan []byte, 64)
	defer func() {
		markDone()
		cancel()
		commandWG.Wait()
		stop, done := context.WithTimeout(context.Background(), r.cfg.ShutdownTimeout.Value())
		defer done()
		if e := transport.Close(stop); e != nil {
			s.Fatal = true
			addError(e.Error())
		}
		s.ConnectionState = "closed"
		s.CommandsRejected += len(commands)
	}()
	handler := func(payload []byte) {
		mu.Lock()
		s.CommandsReceived++
		mu.Unlock()
		select {
		case <-dctx.Done():
			mu.Lock()
			s.CommandsRejected++
			mu.Unlock()
			return
		default:
		}
		select {
		case commands <- payload:
		default:
			mu.Lock()
			s.CommandsRejected++
			mu.Unlock()
			addError("fila de comandos cheia; comando descartado")
		}
	}
	notice := func(n Notice) {
		mu.Lock()
		switch n.Kind {
		case "connected":
			s.ConnectionState = "connected"
			s.Connections++
		case "disconnected":
			s.ConnectionState = "reconnecting"
		case "command_rejected":
			s.CommandsReceived++
			s.CommandsRejected++
		}
		mu.Unlock()
		emit(d.ID, n.Kind, clean(n.Message), 0, 0)
	}
	if e := transport.Start(dctx, handler, notice); e != nil {
		s.Fatal = true
		addError(e.Error())
		return
	}
	commandWG.Add(1)
	go func() {
		defer commandWG.Done()
		for {
			select {
			case <-dctx.Done():
				return
			case payload := <-commands:
				response, e := device.Process(payload, time.Now())
				if e != nil {
					mu.Lock()
					s.CommandsRejected++
					mu.Unlock()
					addError(e.Error())
				}
				if response == nil {
					continue
				}
				emit(d.ID, "command", "comando recebido e processado", 0, 0)
				b, _ := json.Marshal(response)
				op, done := context.WithTimeout(dctx, r.cfg.OperationTimeout.Value())
				receipt, e := transport.Publish(op, device.TopicsFor(d.ID).Responses, b, byte(r.cfg.ResponseQoS))
				done()
				mu.Lock()
				if e != nil {
					s.ResponsesFailed++
				} else {
					s.ResponsesPublished++
					if receipt.Acknowledged {
						s.ResponsesAcknowledged++
					}
				}
				mu.Unlock()
				if e != nil {
					addError(e.Error())
				} else {
					emit(d.ID, "command_response", "command-response publicado: "+response.Status, 0, 0)
				}
			}
		}
	}()
	var seed int64
	if r.cfg.Seed != nil {
		seed = *r.cfg.Seed
	} else {
		var b [8]byte
		_, _ = rand.Read(b[:])
		seed = int64(binary.LittleEndian.Uint64(b[:]))
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(d.ID))
	seed += int64(h.Sum64())
	generator := telemetry.New(d.Telemetry, seed)
	for n := 0; req.Messages == 0 || n < req.Messages; n++ {
		if n > 0 && !wait(dctx, d.Interval.Value()) {
			break
		}
		if dctx.Err() != nil {
			break
		}
		msg := generator.Next(time.Now())
		payload, _ := json.Marshal(msg)
		mu.Lock()
		s.Attempted++
		mu.Unlock()
		op, done := context.WithTimeout(dctx, r.cfg.OperationTimeout.Value())
		receipt, e := transport.Publish(op, device.TopicsFor(d.ID).Telemetry, payload, byte(*d.QoS))
		done()
		mu.Lock()
		if receipt.ReasonCode != nil {
			s.AckReasonCodes[fmt.Sprint(*receipt.ReasonCode)]++
		}
		if e != nil {
			s.Failed++
		} else {
			s.Published++
			if receipt.Acknowledged {
				s.BrokerAcknowledged++
			}
		}
		a, p := s.Attempted, s.Published
		mu.Unlock()
		if e != nil {
			addError(e.Error())
		} else {
			emit(d.ID, "telemetry", fmt.Sprintf("telemetry -> temperature_c=%.2f humidity_pct=%.2f", msg.Temperature, msg.Humidity), a, p)
		}
	}
	markDone()
	if req.Messages > 0 && dctx.Err() == nil {
		select {
		case <-dctx.Done():
		case <-allPublished:
			grace := r.cfg.GracePeriod.Value()
			if req.GracePeriod != nil {
				grace = *req.GracePeriod
			}
			wait(dctx, grace)
		}
	}
	return
}
func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

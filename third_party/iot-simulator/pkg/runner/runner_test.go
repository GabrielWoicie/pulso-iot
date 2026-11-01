package runner

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/woicie/iot-simulator/pkg/config"
	"strings"
	"sync"
	"testing"
	"time"
)

type fake struct {
	mu                   sync.Mutex
	startErr, publishErr error
	blocking             bool
	commands             [][]byte
	topics               []string
	qos                  []byte
	closed               bool
}

func (f *fake) Start(ctx context.Context, h func([]byte), n func(Notice)) error {
	if f.startErr != nil {
		return f.startErr
	}
	n(Notice{Kind: "connected", Message: "connected"})
	for _, b := range f.commands {
		h(b)
	}
	return nil
}
func (f *fake) Publish(ctx context.Context, topic string, payload []byte, q byte) (Receipt, error) {
	if f.blocking {
		<-ctx.Done()
		return Receipt{}, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.topics = append(f.topics, topic)
	f.qos = append(f.qos, q)
	if !json.Valid(payload) {
		return Receipt{}, errors.New("bad JSON")
	}
	if f.publishErr != nil {
		return Receipt{}, f.publishErr
	}
	r := Receipt{Acknowledged: q > 0}
	if q > 0 {
		b := byte(16)
		r.ReasonCode = &b
	}
	return r, nil
}
func (f *fake) Close(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}
func configuration() config.Config {
	c := config.Defaults()
	c.Broker = "mqtts://broker.woicie.com.br:8883"
	c.Interval = config.Duration(time.Millisecond)
	c.GracePeriod = config.Duration(10 * time.Millisecond)
	c.OperationTimeout = config.Duration(50 * time.Millisecond)
	for _, id := range []string{"dev-001", "dev-002", "dev-003"} {
		c.Devices = append(c.Devices, config.Device{ID: id, Password: "TEST-SECRET-PLACEHOLDER", Telemetry: config.Sensors{Temperature: config.Range{Min: 20, Max: 30}, Humidity: config.Range{Min: 40, Max: 80}}})
	}
	if e := c.Normalize(); e != nil {
		panic(e)
	}
	return c
}
func TestSelectionAndOverrides(t *testing.T) {
	c := configuration()
	q := 0
	ds, e := Select(c, RunRequest{DeviceIDs: []string{"dev-001"}, Messages: 20, QoS: &q, Interval: time.Second})
	if e != nil || len(ds) != 1 {
		t.Fatalf("%v %+v", e, ds)
	}
	if *ds[0].QoS != 0 || ds[0].Interval.Value() != time.Second || *c.Devices[0].QoS != 1 || c.Devices[0].Interval.Value() != time.Millisecond {
		t.Fatal("overrides mutate config")
	}
	for _, ids := range [][]string{{"dev-001", "dev-001"}, {"unknown"}} {
		if _, e := Select(c, RunRequest{DeviceIDs: ids}); e == nil {
			t.Fatal(ids)
		}
	}
}
func TestFiniteRunCommandsAndCounters(t *testing.T) {
	c := configuration()
	var mu sync.Mutex
	transports := map[string]*fake{}
	r, e := New(c, func(_ config.Config, d config.Device) Transport {
		f := &fake{commands: [][]byte{[]byte(`{"command_id":"c1","type":"ping"}`), []byte(`{"command_id":"c2","type":"other"}`), []byte(`{`)}}
		mu.Lock()
		transports[d.ID] = f
		mu.Unlock()
		return f
	})
	if e != nil {
		t.Fatal(e)
	}
	events := 0
	result, e := r.Run(context.Background(), RunRequest{Messages: 5}, func(Event) { events++ })
	if e != nil || result.Status != "completed" {
		t.Fatalf("%v %+v", e, result)
	}
	if result.RunID == "" || result.FinishedAt.Before(result.StartedAt) || events == 0 {
		t.Fatal(result)
	}
	for _, d := range result.Devices {
		if d.Attempted != 5 || d.Published != 5 || d.Failed != 0 || d.BrokerAcknowledged != 5 || d.AckReasonCodes["16"] != 5 || d.CommandsReceived != 3 || d.CommandsRejected != 2 || d.ResponsesPublished != 2 || d.ResponsesAcknowledged != 2 || d.ConnectionState != "closed" {
			t.Fatalf("%+v", d)
		}
		f := transports[d.DeviceID]
		if !f.closed {
			t.Fatal("connection leaked")
		}
		for _, topic := range f.topics {
			if !strings.Contains(topic, "/"+d.DeviceID+"/") {
				t.Fatal(topic)
			}
		}
	}
}
func TestQoSZeroHasNoBrokerAcknowledgement(t *testing.T) {
	r, _ := New(configuration(), func(config.Config, config.Device) Transport { return &fake{} })
	q := 0
	result, e := r.Run(context.Background(), RunRequest{Messages: 3, QoS: &q}, nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, d := range result.Devices {
		if d.Published != 3 || d.BrokerAcknowledged != 0 || len(d.AckReasonCodes) != 0 {
			t.Fatal(d)
		}
	}
}
func TestMQTTFailuresAndSanitizedErrors(t *testing.T) {
	for _, startup := range []bool{false, true} {
		r, _ := New(configuration(), func(config.Config, config.Device) Transport {
			f := &fake{}
			if startup {
				f.startErr = errors.New("bad auth TEST-SECRET-PLACEHOLDER")
			} else {
				f.publishErr = errors.New("publish TEST-SECRET-PLACEHOLDER")
			}
			return f
		})
		result, e := r.Run(context.Background(), RunRequest{Messages: 3}, nil)
		if e == nil || result.Status != "failed" {
			t.Fatal(result, e)
		}
		for _, d := range result.Devices {
			if startup {
				if !d.Fatal || d.Attempted != 0 {
					t.Fatal(d)
				}
			} else if d.Failed != 3 || d.Published != 0 {
				t.Fatal(d)
			}
			for _, msg := range d.Errors {
				if strings.Contains(msg, "TEST-SECRET-PLACEHOLDER") {
					t.Fatal("secret leak")
				}
			}
		}
	}
}
func TestCancellationAndDeadline(t *testing.T) {
	for _, messages := range []int{0, 100} {
		r, _ := New(configuration(), func(config.Config, config.Device) Transport { return &fake{blocking: true} })
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
		start := time.Now()
		result, e := r.Run(ctx, RunRequest{Messages: messages}, nil)
		cancel()
		if !errors.Is(e, context.DeadlineExceeded) || result.Status != "cancelled" || result.Reason == "" || time.Since(start) > time.Second {
			t.Fatal(result, e)
		}
	}
}
func TestCancelledBeforeStart(t *testing.T) {
	r, _ := New(configuration(), func(config.Config, config.Device) Transport { return &fake{} })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, e := r.Run(ctx, RunRequest{Messages: 10}, nil)
	if !errors.Is(e, context.Canceled) || result.Status != "cancelled" {
		t.Fatal(e, result)
	}
	for _, d := range result.Devices {
		if d.Attempted != 0 {
			t.Fatal(d)
		}
	}
}
func TestBoundedErrors(t *testing.T) {
	c := configuration()
	r, _ := New(c, func(config.Config, config.Device) Transport { return &fake{publishErr: errors.New("failure")} })
	result, _ := r.Run(context.Background(), RunRequest{Messages: 110, Interval: time.Nanosecond}, nil)
	for _, d := range result.Devices {
		if len(d.Errors) != 100 || d.ErrorsTotal != 110 {
			t.Fatal(d)
		}
	}
}

package app

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/packets"
	"github.com/eclipse/paho.golang/paho"
	"github.com/jackc/pgx/v5"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Publisher interface {
	Publish(context.Context, string, []byte, byte) error
	Ready() bool
}
type Metrics struct{ Received, Dropped, Invalid, PersistenceFailed atomic.Uint64 }
type Incoming struct {
	Topic   string
	Payload []byte
	QoS     byte
	Retain  bool
}
type MQTT struct {
	cm            *autopaho.ConnectionManager
	ready         atomic.Bool
	queue         chan Incoming
	app           *App
	mu            sync.Mutex
	closed        bool
	epoch         uint64
	subscriptions sync.WaitGroup
}

func (a *App) StartMQTT() (*MQTT, error) {
	m := &MQTT{queue: make(chan Incoming, 1024), app: a}
	u, e := url.Parse(a.C.MQTTBroker)
	if e != nil {
		return nil, e
	}
	u.Scheme = "tls"
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}
	cfg := autopaho.ClientConfig{ServerUrls: []*url.URL{u}, TlsCfg: tc, KeepAlive: 30, CleanStartOnInitialConnection: true, SessionExpiryInterval: 0, ConnectTimeout: 8 * time.Second, ReconnectBackoff: autopaho.NewExponentialBackoff(time.Second, 30*time.Second, 2*time.Second, 2), ConnectUsername: a.C.MQTTUser, ConnectPassword: []byte(a.C.MQTTPassword), ClientConfig: paho.ClientConfig{ClientID: a.C.MQTTClientID, OnPublishReceived: []func(paho.PublishReceived) (bool, error){func(v paho.PublishReceived) (bool, error) {
		p := v.Packet
		if p.Retain || len(p.Payload) > 65536 {
			a.Metrics.Invalid.Add(1)
			return true, nil
		}
		msg := Incoming{p.Topic, append([]byte(nil), p.Payload...), p.QoS, p.Retain}
		select {
		case m.queue <- msg:
		default:
			a.Metrics.Dropped.Add(1)
		}
		return true, nil
	}}}, OnConnectionDown: func() bool { m.mu.Lock(); m.epoch++; m.ready.Store(false); m.mu.Unlock(); return a.ctx.Err() == nil }}
	cfg.AttemptConnection = func(ctx context.Context, _ autopaho.ClientConfig, u *url.URL) (net.Conn, error) {
		d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 8 * time.Second}, Config: tc}
		c, e := d.DialContext(ctx, "tcp", u.Host)
		if e != nil {
			return nil, e
		}
		return packets.NewThreadSafeConn(&writeDeadlineConn{c}), nil
	}
	cfg.OnConnectionUp = func(cm *autopaho.ConnectionManager, _ *paho.Connack) {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return
		}
		m.epoch++
		epoch := m.epoch
		m.subscriptions.Add(1)
		m.mu.Unlock()
		go func() {
			defer m.subscriptions.Done()
			ctx, cancel := context.WithTimeout(a.ctx, 8*time.Second)
			defer cancel()
			ack, e := cm.Subscribe(ctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: Prefix + "+/telemetry", QoS: 1, RetainHandling: 2}, {Topic: Prefix + "+/command-responses", QoS: 1, RetainHandling: 2}}})
			good := e == nil && ack != nil && len(ack.Reasons) == 2
			if good {
				for _, v := range ack.Reasons {
					good = good && v < 128
				}
			}

			m.mu.Lock()
			defer m.mu.Unlock()
			if !m.closed && m.epoch == epoch {
				m.ready.Store(good)
				if !good {
					slog.Warn("MQTT subscription failed; reconnect or review service ACL")
				}
			}
		}()
	}
	m.cm, e = autopaho.NewConnection(a.ctx, cfg)
	if e != nil {
		return nil, errors.New("não foi possível iniciar MQTT")
	}
	for i := 0; i < 4; i++ {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			for {
				select {
				case <-a.ctx.Done():
					return
				case v := <-m.queue:
					ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
					if e := a.Ingest(ctx, v); e != nil {
						a.Metrics.PersistenceFailed.Add(1)
					}
					cancel()
				}
			}
		}()
	}
	a.MQTT = m
	return m, nil
}

type writeDeadlineConn struct{ net.Conn }

func (c *writeDeadlineConn) Write(p []byte) (int, error) {
	if e := c.SetWriteDeadline(time.Now().Add(8 * time.Second)); e != nil {
		return 0, e
	}
	return c.Conn.Write(p)
}
func (m *MQTT) Ready() bool { return m.ready.Load() }
func (m *MQTT) Publish(ctx context.Context, topic string, payload []byte, q byte) error {
	if !m.Ready() {
		return errors.New("MQTT não está pronto")
	}
	ack, e := m.cm.Publish(ctx, &paho.Publish{Topic: topic, Payload: payload, QoS: q, Retain: false})
	if e != nil || q > 0 && (ack == nil || ack.ReasonCode >= 128) {
		return errors.New("MQTT publish falhou; entrega indeterminada")
	}
	return nil
}
func (m *MQTT) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	m.ready.Store(false)
	m.mu.Unlock()
	e := m.cm.Disconnect(ctx)
	m.subscriptions.Wait()
	return e
}
func VerifyCredential(ctx context.Context, c Config, id, password string) error {
	u, e := url.Parse(c.MQTTBroker)
	if e != nil {
		return e
	}
	u.Scheme = "tls"
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cm, e := autopaho.NewConnection(ctx, autopaho.ClientConfig{ServerUrls: []*url.URL{u}, TlsCfg: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}, KeepAlive: 10, ConnectTimeout: 5 * time.Second, CleanStartOnInitialConnection: true, ConnectUsername: id, ConnectPassword: []byte(password), ClientConfig: paho.ClientConfig{ClientID: id}})
	if e != nil {
		return e
	}
	e = cm.AwaitConnection(ctx)
	stop, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	_ = cm.Disconnect(stop)
	return e
}

type messagePayload struct {
	DeviceID    string   `json:"device_id"`
	MessageID   string   `json:"message_id"`
	CommandID   string   `json:"command_id"`
	Timestamp   string   `json:"timestamp"`
	Temperature *float64 `json:"temperature_c"`
	Humidity    *float64 `json:"humidity_pct"`
	Status      string   `json:"status"`
}

func validateIncoming(v Incoming) (string, string, messagePayload, bool) {
	var p messagePayload
	if v.Retain || v.QoS > 2 || len(v.Payload) > 65536 || !strings.HasPrefix(v.Topic, Prefix) {
		return "", "", p, false
	}
	parts := strings.Split(strings.TrimPrefix(v.Topic, Prefix), "/")
	if len(parts) != 2 || !validDeviceID(parts[0]) || parts[1] != "telemetry" && parts[1] != "command-responses" {
		return "", "", p, false
	}
	if e := json.Unmarshal(v.Payload, &p); e != nil {
		return "", "", p, false
	}
	if p.DeviceID != "" && p.DeviceID != parts[0] || len(p.MessageID) > 120 || len(p.CommandID) > 120 {
		return "", "", p, false
	}
	if _, e := time.Parse(time.RFC3339Nano, p.Timestamp); e != nil {
		return "", "", p, false
	}
	if parts[1] == "telemetry" {
		if p.Temperature == nil || p.Humidity == nil || *p.Temperature < -100 || *p.Temperature > 150 || *p.Humidity < 0 || *p.Humidity > 100 {
			return "", "", p, false
		}
	} else {
		if p.CommandID == "" || p.Status != "executed" && p.Status != "unsupported" && p.Status != "failed" {
			return "", "", p, false
		}
	}
	return parts[0], parts[1], p, true
}
func (a *App) Ingest(ctx context.Context, v Incoming) error {
	identifier, kind, p, ok := validateIncoming(v)
	if !ok {
		a.Metrics.Invalid.Add(1)
		return nil
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var id string
	e = tx.QueryRow(ctx, "SELECT id::text FROM pulso_iot.devices WHERE device_identifier=$1 AND deleted_at IS NULL AND status='active'", identifier).Scan(&id)
	if e != nil {
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		a.Metrics.Invalid.Add(1)
		return nil
	}
	sent, _ := time.Parse(time.RFC3339Nano, p.Timestamp)
	tag, e := tx.Exec(ctx, `INSERT INTO pulso_iot.mqtt_messages(device_id,topic,message_kind,direction,message_id,command_id,payload,qos,sent_at) VALUES($1,$2,$3,'inbound',NULLIF($4,''),NULLIF($5,''),$6,$7,$8) ON CONFLICT DO NOTHING`, id, v.Topic, kind, p.MessageID, p.CommandID, json.RawMessage(v.Payload), v.QoS, sent)
	if e != nil {
		return e
	}
	if tag.RowsAffected() > 0 {
		if _, e = tx.Exec(ctx, "UPDATE pulso_iot.devices SET last_seen_at=now() WHERE id=$1", id); e != nil {
			return e
		}
		if kind == "command-responses" {
			_, e = tx.Exec(ctx, `UPDATE pulso_iot.device_commands SET response_payload=$3,responded_at=now(),status=$4 WHERE device_id=$1 AND command_id=$2 AND status IN ('pending','published')`, id, p.CommandID, json.RawMessage(v.Payload), p.Status)
			if e != nil {
				return e
			}
		}
	}
	if e = tx.Commit(ctx); e == nil {
		a.Metrics.Received.Add(uint64(tag.RowsAffected()))
	}
	return e
}

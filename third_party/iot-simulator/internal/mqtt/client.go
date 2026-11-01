// Package mqtt adapts the Eclipse Paho MQTT 5 client to a small testable interface.
package mqtt

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/packets"
	"github.com/eclipse/paho.golang/paho"
	"github.com/woicie/iot-simulator/internal/device"
	"github.com/woicie/iot-simulator/pkg/config"
)

type Receipt struct {
	Acknowledged bool
	ReasonCode   *byte
}
type Notice struct{ Kind, Message string }

// Transport implementations must honor context cancellation and stop callbacks before Close returns.
type Transport interface {
	Start(context.Context, func([]byte), func(Notice)) error
	Publish(context.Context, string, []byte, byte) (Receipt, error)
	Close(context.Context) error
}
type Client struct {
	callbackMu      sync.Mutex
	closed          bool
	cfg             config.Config
	device          config.Device
	manager         *autopaho.ConnectionManager
	mu              sync.Mutex
	ready           bool
	epoch           uint64
	changed         chan struct{}
	subscriptionErr error
	workers         sync.WaitGroup
	notify          func(Notice)
}

func New(c config.Config, d config.Device) *Client {
	return &Client{cfg: c, device: d, changed: make(chan struct{})}
}
func (c *Client) state(ready bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ready = ready
	c.subscriptionErr = err
	close(c.changed)
	c.changed = make(chan struct{})
}
func (c *Client) WaitReady(ctx context.Context) error {
	for {
		c.mu.Lock()
		ready, e, ch := c.ready, c.subscriptionErr, c.changed
		c.mu.Unlock()
		if e != nil {
			return e
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

// Backoff returns 0 for first attempt, then 1s, 2s, 4s ... capped at max.
func Backoff(max time.Duration) func(int) time.Duration {
	return func(n int) time.Duration {
		if n <= 0 {
			return 0
		}
		d := time.Second
		for i := 1; i < n && d < max; i++ {
			if d > max/2 {
				return max
			}
			d *= 2
		}
		if d > max {
			return max
		}
		return d
	}
}
func (c *Client) Start(ctx context.Context, handler func([]byte), notify func(Notice)) error {
	originalHandler, originalNotify := handler, notify
	handler = func(b []byte) {
		c.callbackMu.Lock()
		defer c.callbackMu.Unlock()
		if !c.closed {
			originalHandler(b)
		}
	}
	notify = func(n Notice) {
		c.callbackMu.Lock()
		defer c.callbackMu.Unlock()
		if !c.closed {
			originalNotify(n)
		}
	}
	c.notify = notify
	u, e := url.Parse(c.cfg.Broker)
	if e != nil {
		return errors.New("endereço do broker inválido")
	}
	u.Scheme = "tls"
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}
	cfg := autopaho.ClientConfig{
		ServerUrls: []*url.URL{u}, TlsCfg: tlsCfg, KeepAlive: 30,
		CleanStartOnInitialConnection: true, SessionExpiryInterval: 0,
		ConnectTimeout: c.cfg.OperationTimeout.Value(), ReconnectBackoff: Backoff(c.cfg.ReconnectMax.Value()),
		ConnectUsername: c.device.Username, ConnectPassword: []byte(c.device.Password),
		ConnectPacketBuilder: func(p *paho.Connect, _ *url.URL) (*paho.Connect, error) {
			p.CleanStart = true
			zero := uint32(0)
			if p.Properties == nil {
				p.Properties = &paho.ConnectProperties{}
			}
			p.Properties.SessionExpiryInterval = &zero
			return p, nil
		},
		// Bound TLS socket writes too, including QoS 0 writes that Paho performs synchronously.
		AttemptConnection: func(ctx context.Context, _ autopaho.ClientConfig, u *url.URL) (net.Conn, error) {
			dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: c.cfg.OperationTimeout.Value()}, Config: tlsCfg}
			conn, e := dialer.DialContext(ctx, "tcp", u.Host)
			if e != nil {
				return nil, e
			}
			return packets.NewThreadSafeConn(&writeTimeoutConn{Conn: conn, timeout: c.cfg.OperationTimeout.Value()}), nil
		},
		OnConnectError: func(err error) {
			var denied *autopaho.ConnackError
			if errors.As(err, &denied) {
				notify(Notice{"connect_error", fmt.Sprintf("conexão recusada pelo broker (CONNACK %d)", denied.ReasonCode)})
			} else {
				notify(Notice{"connect_error", "conexão falhou; verifique rede, DNS, TLS e credenciais; nova tentativa automática"})
			}
		},
		OnConnectionDown: func() bool {
			c.mu.Lock()
			c.epoch++
			c.mu.Unlock()
			c.state(false, nil)
			notify(Notice{"disconnected", "desconectado; aguardando reconexão"})
			return ctx.Err() == nil
		},
		ClientConfig: paho.ClientConfig{ClientID: c.device.ClientID, OnPublishReceived: []func(paho.PublishReceived) (bool, error){func(pr paho.PublishReceived) (bool, error) {
			if pr.Packet.Topic != device.TopicsFor(c.device.ID).Commands {
				return false, nil
			}
			if pr.Packet.Retain {
				notify(Notice{"command_rejected", "comando retido ignorado"})
				return true, nil
			}
			if len(pr.Packet.Payload) > 65536 {
				notify(Notice{"command_rejected", "comando excede 64 KiB"})
				return true, nil
			}
			handler(append([]byte(nil), pr.Packet.Payload...))
			return true, nil
		}}},
	}
	cfg.OnConnectionUp = func(cm *autopaho.ConnectionManager, _ *paho.Connack) {
		c.mu.Lock()
		c.epoch++
		epoch := c.epoch
		c.mu.Unlock()
		notify(Notice{"connected", "conectado ao broker"})
		c.workers.Add(1)
		go func() {
			defer c.workers.Done()
			subctx, cancel := context.WithTimeout(ctx, c.cfg.OperationTimeout.Value())
			defer cancel()
			ack, err := cm.Subscribe(subctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: device.TopicsFor(c.device.ID).Commands, QoS: 1, RetainHandling: 2}}})
			if err != nil || ack == nil || len(ack.Reasons) != 1 || ack.Reasons[0] > 2 {
				err = errors.New("assinatura de commands recusada ou sem confirmação")
			}
			c.mu.Lock()
			if epoch != c.epoch {
				c.mu.Unlock()
				return
			}
			c.ready = err == nil
			c.subscriptionErr = err
			close(c.changed)
			c.changed = make(chan struct{})
			c.mu.Unlock()
			if err != nil {
				notify(Notice{"subscribe_error", err.Error()})
			} else {
				notify(Notice{"subscribed", "inscrito em commands"})
			}
		}()
	}
	cm, e := autopaho.NewConnection(ctx, cfg)
	if e != nil {
		return errors.New("falha ao iniciar cliente MQTT")
	}
	c.manager = cm
	startup, cancel := context.WithTimeout(ctx, c.cfg.ConnectTimeout.Value())
	defer cancel()
	if e = c.WaitReady(startup); e != nil {
		return fmt.Errorf("conexão/assinatura não ficou pronta: %w", e)
	}
	return nil
}
func (c *Client) Publish(ctx context.Context, topic string, payload []byte, qos byte) (Receipt, error) {
	t := device.TopicsFor(c.device.ID)
	if topic != t.Telemetry && topic != t.Responses {
		return Receipt{}, errors.New("publicação fora dos tópicos do dispositivo")
	}
	if qos > 2 {
		return Receipt{}, errors.New("QoS inválido")
	}
	if e := c.WaitReady(ctx); e != nil {
		return Receipt{}, e
	}
	p, e := c.manager.Publish(ctx, &paho.Publish{Topic: topic, Payload: payload, QoS: qos, Retain: false})
	r := Receipt{}
	if qos > 0 && p != nil {
		code := p.ReasonCode
		r.ReasonCode = &code
		r.Acknowledged = e == nil && code < 128
	}
	if e != nil {
		return r, errors.New("publicação MQTT falhou ou expirou; entrega pode ser indeterminada")
	}
	if p != nil && p.ReasonCode >= 128 {
		return r, fmt.Errorf("broker recusou publicação (reason code %d)", p.ReasonCode)
	}
	return r, nil
}
func (c *Client) Close(ctx context.Context) error {
	c.callbackMu.Lock()
	c.closed = true
	c.callbackMu.Unlock()
	if c.manager == nil {
		return nil
	}
	e := c.manager.Disconnect(ctx)
	if e != nil {
		return errors.New("prazo de desconexão excedido")
	}
	c.workers.Wait()
	return nil
}

type writeTimeoutConn struct {
	net.Conn
	timeout time.Duration
}

func (c *writeTimeoutConn) Write(p []byte) (int, error) {
	if e := c.Conn.SetWriteDeadline(time.Now().Add(c.timeout)); e != nil {
		return 0, e
	}
	return c.Conn.Write(p)
}

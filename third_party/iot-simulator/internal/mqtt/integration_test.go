//go:build integration

package mqtt_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/woicie/iot-simulator/internal/mqtt"
	"github.com/woicie/iot-simulator/pkg/config"
	"github.com/woicie/iot-simulator/pkg/runner"
)

// Requires a local mosquitto binary. All keys/passwords are temporary test-only material.
// No public broker, DNS changes, EMQX admin API or real credentials are used.
func TestIntegrationTLSMQTT5(t *testing.T) {
	binaryPath := os.Getenv("MQTT_TEST_BROKER_PATH")
	if binaryPath == "" {
		t.Skip("set MQTT_TEST_BROKER_PATH to local mosquitto binary")
	}
	if conn, e := net.DialTimeout("tcp", "127.0.0.1:8883", 100*time.Millisecond); e == nil {
		conn.Close()
		t.Fatal("port 8883 already in use; do not touch an existing broker")
	}
	temp := t.TempDir()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost test CA"}, DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	certPath := filepath.Join(temp, "cert.pem")
	keyPath := filepath.Join(temp, "key.pem")
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600)
	t.Setenv("SSL_CERT_FILE", certPath)
	// Plaintext password input is test-only; mosquitto_passwd converts it to a salted hash before startup.
	passwordPath := filepath.Join(temp, "passwords")
	os.WriteFile(passwordPath, []byte("dev-001:test-placeholder\ndev-002:test-placeholder\nsvc-backend:test-placeholder\n"), 0600)
	passwd := filepath.Join(filepath.Dir(binaryPath), "mosquitto_passwd")
	if _, e = os.Stat(passwd); e != nil {
		passwd = filepath.Join(filepath.Dir(filepath.Dir(binaryPath)), "bin", "mosquitto_passwd")
	}
	if out, e := exec.Command(passwd, "-U", passwordPath).CombinedOutput(); e != nil {
		t.Fatalf("mosquitto_passwd: %v %s", e, out)
	}
	aclPath := filepath.Join(temp, "acl")
	var acl strings.Builder
	for _, id := range []string{"dev-001", "dev-002"} {
		fmt.Fprintf(&acl, "user %s\ntopic write desbravatech/v1/devices/%s/telemetry\ntopic read desbravatech/v1/devices/%s/commands\ntopic write desbravatech/v1/devices/%s/command-responses\n", id, id, id, id)
	}
	acl.WriteString("user svc-backend\ntopic read desbravatech/v1/devices/+/telemetry\ntopic write desbravatech/v1/devices/+/commands\ntopic read desbravatech/v1/devices/+/command-responses\n")
	os.WriteFile(aclPath, []byte(acl.String()), 0600)
	conf := filepath.Join(temp, "mosquitto.conf")
	os.WriteFile(conf, []byte(fmt.Sprintf("user root\nlistener 8883 127.0.0.1\nallow_anonymous false\npassword_file %s\nacl_file %s\ncertfile %s\nkeyfile %s\npersistence false\n", passwordPath, aclPath, certPath, keyPath)), 0600)
	startBroker := func() *exec.Cmd {
		t.Helper()
		cmd := exec.Command(binaryPath, "-c", conf)
		var logs bytes.Buffer
		cmd.Stdout = &logs
		cmd.Stderr = &logs
		if e := cmd.Start(); e != nil {
			t.Fatal(e)
		}
		for i := 0; i < 100; i++ {
			if c, e := net.DialTimeout("tcp", "127.0.0.1:8883", 20*time.Millisecond); e == nil {
				c.Close()
				return cmd
			}
			time.Sleep(20 * time.Millisecond)
		}
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf("broker not ready: %s", logs.String())
		return nil
	}
	broker := startBroker()
	defer func() {
		if broker != nil {
			broker.Process.Kill()
			broker.Wait()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	u, _ := url.Parse("tls://localhost:8883")
	ready := make(chan struct{}, 1)
	received := make(chan *paho.Publish, 64)
	backend, e := autopaho.NewConnection(ctx, autopaho.ClientConfig{ServerUrls: []*url.URL{u}, TlsCfg: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "localhost"}, KeepAlive: 10, CleanStartOnInitialConnection: true, SessionExpiryInterval: 0, ConnectUsername: "svc-backend", ConnectPassword: []byte("test-placeholder"), OnConnectionUp: func(cm *autopaho.ConnectionManager, _ *paho.Connack) {
		go func() {
			ack, e := cm.Subscribe(ctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: "desbravatech/v1/devices/+/telemetry", QoS: 2}, {Topic: "desbravatech/v1/devices/+/command-responses", QoS: 1}}})
			if e == nil && len(ack.Reasons) == 2 && ack.Reasons[0] < 128 && ack.Reasons[1] < 128 {
				select {
				case ready <- struct{}{}:
				default:
				}
			}
		}()
	}, ClientConfig: paho.ClientConfig{ClientID: "svc-backend", OnPublishReceived: []func(paho.PublishReceived) (bool, error){func(pr paho.PublishReceived) (bool, error) { received <- pr.Packet; return true, nil }}}})
	if e != nil {
		t.Fatal(e)
	}
	defer backend.Disconnect(context.Background())
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("backend did not connect")
	}
	c := config.Defaults()
	c.Broker = "mqtts://localhost:8883"
	c.Interval = config.Duration(30 * time.Millisecond)
	c.GracePeriod = config.Duration(300 * time.Millisecond)
	c.ConnectTimeout = config.Duration(2 * time.Second)
	c.OperationTimeout = config.Duration(time.Second)
	for _, id := range []string{"dev-001", "dev-002"} {
		c.Devices = append(c.Devices, config.Device{ID: id, Password: "test-placeholder", Telemetry: config.Sensors{Temperature: config.Range{Min: 20, Max: 30}, Humidity: config.Range{Min: 40, Max: 80}}})
	}
	for _, q := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("qos%d", q), func(t *testing.T) {
			r, e := runner.New(c, nil)
			if e != nil {
				t.Fatal(e)
			}
			var sent sync.WaitGroup
			sendErrors := make(chan error, 2)
			result, e := r.Run(ctx, runner.RunRequest{Messages: 3, QoS: &q}, func(ev runner.Event) {
				if ev.Kind == "subscribed" {
					sent.Add(1)
					go func() {
						defer sent.Done()
						_, e := backend.Publish(ctx, &paho.Publish{Topic: "desbravatech/v1/devices/" + ev.DeviceID + "/commands", QoS: 1, Payload: []byte(`{"command_id":"cmd-integration","type":"ping"}`)})
						if e != nil {
							sendErrors <- e
						}
					}()
				}
			})
			sent.Wait()
			close(sendErrors)
			for err := range sendErrors {
				t.Error(err)
			}
			if e != nil || result.Status != "completed" {
				t.Fatalf("%v %+v", e, result)
			}
			for _, d := range result.Devices {
				if d.Attempted != 3 || d.Published != 3 || d.CommandsReceived != 1 || d.ResponsesPublished != 1 || d.ResponsesAcknowledged != 1 {
					t.Fatalf("%+v", d)
				}
				if q == 0 && d.BrokerAcknowledged != 0 || q > 0 && d.BrokerAcknowledged != 3 {
					t.Fatal(d)
				}
			}
			telemetryCount, responses := 0, 0
			deadline := time.NewTimer(2 * time.Second)
			defer deadline.Stop()
			for telemetryCount < 6 || responses < 2 {
				select {
				case m := <-received:
					if m.Retain {
						t.Error("unexpected retain")
					}
					if strings.HasSuffix(m.Topic, "/telemetry") {
						telemetryCount++
					} else {
						var response struct{ CommandID, Status string }
						var body map[string]string
						json.Unmarshal(m.Payload, &body)
						response.CommandID = body["command_id"]
						response.Status = body["status"]
						if response.CommandID != "cmd-integration" || response.Status != "executed" {
							t.Error(body)
						}
						responses++
					}
				case <-deadline.C:
					t.Fatalf("received telemetry=%d responses=%d", telemetryCount, responses)
				}
			}
		})
	}
	t.Run("cli-finite", func(t *testing.T) {
		cfgPath := filepath.Join(temp, "cli.yaml")
		reportPath := filepath.Join(temp, "cli-result.json")
		yaml := `broker: "mqtts://localhost:8883"
connect_timeout: 2s
operation_timeout: 1s
grace_period: 50ms
devices:
  - id: dev-001
    password: "${SIM_TEST_PASSWORD}"
    telemetry:
      temperature_c: {min: 20, max: 30}
      humidity_pct: {min: 40, max: 80}
  - id: dev-002
    password: "${SIM_TEST_PASSWORD}"
    telemetry:
      temperature_c: {min: 21, max: 31}
      humidity_pct: {min: 45, max: 80}
`
		os.WriteFile(cfgPath, []byte(yaml), 0600)
		command := exec.CommandContext(ctx, "go", "run", "../../cmd/tcc-simulator", "run", "--config", cfgPath, "--devices", "dev-001,dev-002", "--messages", "2", "--interval", "10ms", "--qos", "1", "--output-json", reportPath)
		command.Env = append(os.Environ(), "SIM_TEST_PASSWORD=test-placeholder")
		output, e := command.CombinedOutput()
		if e != nil {
			t.Fatalf("CLI failed: %v %s", e, output)
		}
		if bytes.Contains(output, []byte("test-placeholder")) {
			t.Fatal("CLI leaked test password")
		}
		data, e := os.ReadFile(reportPath)
		if e != nil {
			t.Fatal(e)
		}
		var result runner.RunResult
		if e = json.Unmarshal(data, &result); e != nil {
			t.Fatal(e)
		}
		if result.Status != "completed" || len(result.Devices) != 2 {
			t.Fatal(result)
		}
		for _, d := range result.Devices {
			if d.Attempted != 2 || d.Published != 2 || d.BrokerAcknowledged != 2 {
				t.Fatal(d)
			}
		}
		t.Logf("Real CLI output against isolated local TLS broker:\n%s", output)
	})

	t.Run("invalid-password", func(t *testing.T) {
		d := c.Devices[0]
		d.ClientID = d.ID
		d.Username = d.ID
		d.Password = "wrong-test-placeholder"
		client := mqtt.New(c, d)
		subctx, done := context.WithTimeout(ctx, 600*time.Millisecond)
		defer done()
		e := client.Start(subctx, func([]byte) {}, func(mqtt.Notice) {})
		client.Close(context.Background())
		if e == nil {
			t.Fatal("bad password accepted")
		}
	})
	t.Run("hostname-validation", func(t *testing.T) {
		conn, e := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", "127.0.0.1:8883", &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "wrong.invalid"})
		if e == nil {
			conn.Close()
			t.Fatal("invalid hostname accepted")
		}
	})
	t.Run("reconnect-and-resubscribe", func(t *testing.T) {
		d := c.Devices[0]
		d.ClientID = d.ID
		d.Username = d.ID
		client := mqtt.New(c, d)
		notices := make(chan string, 16)
		if e := client.Start(ctx, func([]byte) {}, func(n mqtt.Notice) {
			select {
			case notices <- n.Kind:
			default:
			}
		}); e != nil {
			t.Fatal(e)
		}
		defer client.Close(context.Background())
		// Consume initial subscribed notification (Start readiness can precede its callback).
		for {
			select {
			case kind := <-notices:
				if kind == "subscribed" {
					goto initialReady
				}
			case <-ctx.Done():
				t.Fatal("initial subscribe missing")
			}
		}
	initialReady:
		broker.Process.Kill()
		broker.Wait()
		broker = nil
		for {
			select {
			case kind := <-notices:
				if kind == "disconnected" {
					goto disconnected
				}
			case <-ctx.Done():
				t.Fatal("disconnect missing")
			}
		}
	disconnected:
		broker = startBroker()
		for {
			select {
			case kind := <-notices:
				if kind == "subscribed" {
					goto reconnected
				}
			case <-ctx.Done():
				t.Fatal("resubscribe missing")
			}
		}
	reconnected:
		op, done := context.WithTimeout(ctx, 2*time.Second)
		defer done()
		receipt, e := client.Publish(op, "desbravatech/v1/devices/dev-001/telemetry", []byte(`{"test":"reconnected"}`), 1)
		if e != nil || !receipt.Acknowledged {
			t.Fatal(receipt, e)
		}
	})
}

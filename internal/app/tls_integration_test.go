//go:build integration

package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tlsBroker(t *testing.T) {
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
	t.Cleanup(func() {
		if broker != nil {
			broker.Process.Kill()
			broker.Wait()
		}
	})
}
func TestPlatformTLSMQTT5(t *testing.T) {
	if os.Getenv("MQTT_TEST_BROKER_PATH") == "" {
		t.Skip("set MQTT_TEST_BROKER_PATH for local Mosquitto TLS integration")
	}
	tlsBroker(t)
	a, _ := fixture(t)
	h := a.Handler()
	token := loginToken(t, h, "admin@example.com", "Admin-pass!1234")
	ids := []string{}
	for _, name := range []string{"dev-001", "dev-002"} {
		v := req(t, h, "POST", "/devices", token, map[string]any{"name": "TLS Sensor", "device_identifier": name, "device_mode": "simulated", "mqtt_password": "test-placeholder"}, 201)["data"].(map[string]any)
		ids = append(ids, v["id"].(string))
	}
	mqtt, e := a.StartMQTT()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		mqtt.Close(ctx)
	})
	end := time.Now().Add(8 * time.Second)
	for !mqtt.Ready() && time.Now().Before(end) {
		time.Sleep(20 * time.Millisecond)
	}
	if !mqtt.Ready() {
		t.Fatal("MQTT subscription not ready")
	}
	testReq := map[string]any{"device_ids": []string{"dev-001", "dev-002"}, "messages": 20, "interval_ms": 100, "qos": 1}
	v := req(t, h, "POST", "/tests", token, testReq, 202)["data"].(map[string]any)
	runID := v["id"].(string)
	end = time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		var n int
		a.DB.QueryRow(context.Background(), "SELECT count(DISTINCT device_id) FROM pulso_iot.mqtt_messages WHERE message_kind='telemetry'").Scan(&n)
		if n == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	command := req(t, h, "POST", "/devices/"+ids[0]+"/commands", token, map[string]string{"type": "ping"}, 201)["data"].(map[string]any)
	end = time.Now().Add(4 * time.Second)
	executed := false
	for time.Now().Before(end) {
		var status string
		a.DB.QueryRow(context.Background(), "SELECT status FROM pulso_iot.device_commands WHERE id=$1", command["id"]).Scan(&status)
		if status == "executed" {
			executed = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !executed {
		t.Fatal("ping response missing")
	}
	waitTest(t, a, runID, "completed")
	for _, id := range ids {
		var messages int
		var ack int
		var received *int
		a.DB.QueryRow(context.Background(), "SELECT published,acknowledged_by_broker,received_by_backend FROM pulso_iot.test_results WHERE test_run_id=$1 AND device_id=$2", runID, id).Scan(&messages, &ack, &received)
		if messages != 20 || ack != 20 || received != nil {
			t.Fatalf("counters %d %d %v", messages, ack, received)
		}
		a.DB.QueryRow(context.Background(), "SELECT count(*) FROM pulso_iot.mqtt_messages WHERE device_id=$1 AND message_kind='telemetry'", id).Scan(&messages)
		if messages != 20 {
			t.Fatal("received telemetry", messages)
		}
	}
	// Explicit current-password import performs a genuine MQTT 5 TLS CONNECT.
	req(t, h, "POST", "/devices/"+ids[0]+"/credentials/import", token, map[string]string{"mqtt_password": "test-placeholder"}, 200)
	req(t, h, "GET", "/health", "", nil, 200)
}

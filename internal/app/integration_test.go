//go:build integration

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	simconfig "github.com/woicie/iot-simulator/pkg/config"
	"github.com/woicie/iot-simulator/pkg/runner"
	"github.com/woicie/pulso-iot/internal/emqx"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeBroker struct {
	mu     sync.Mutex
	users  map[string]string
	online map[string]bool
	fail   bool
}

func (f *fakeBroker) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		w.WriteHeader(503)
		return
	}
	p := r.URL.Path
	if strings.Contains(p, "/authentication/") && strings.Contains(p, "/users/") {
		id := p[strings.LastIndex(p, "/")+1:]
		switch r.Method {
		case "GET":
			if _, ok := f.users[id]; !ok {
				w.WriteHeader(404)
				return
			}
			w.Write([]byte(`{}`))
		case "PUT":
			var b struct {
				Password string `json:"password"`
			}
			json.NewDecoder(r.Body).Decode(&b)
			f.users[id] = b.Password
			w.WriteHeader(204)
		case "DELETE":
			delete(f.users, id)
			w.WriteHeader(204)
		}
		return
	}
	if strings.HasSuffix(p, "/users") && r.Method == "POST" {
		var b struct {
			ID       string `json:"user_id"`
			Password string `json:"password"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		f.users[b.ID] = b.Password
		w.WriteHeader(201)
		return
	}
	if strings.Contains(p, "/rules/clients/") {
		w.WriteHeader(204)
		return
	}
	if strings.Contains(p, "/clients/") {
		id := p[strings.LastIndex(p, "/")+1:]
		if r.Method == "DELETE" {
			f.online[id] = false
			w.WriteHeader(204)
			return
		}
		if !f.online[id] {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(`{"connected":true}`))
		return
	}
	if p == "/api/v5/clients" {
		w.Write([]byte(`{"meta":{"count":0}}`))
		return
	}
	w.WriteHeader(404)
}
func fixture(t *testing.T) (*App, *fakeBroker) {
	t.Helper()
	u := os.Getenv("PULSO_TEST_DATABASE_URL")
	if u == "" {
		t.Skip("set PULSO_TEST_DATABASE_URL to an empty disposable PostgreSQL database")
	}
	ctx := context.Background()
	db, e := Connect(ctx, u)
	if e != nil {
		t.Fatal(e)
	}
	var exists bool
	if e = db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname='pulso_iot')").Scan(&exists); e != nil {
		t.Fatal(e)
	}
	if exists {
		db.Close()
		t.Fatal("integration refuses existing pulso_iot schema; use an EMPTY disposable database")
	}
	sql, e := os.ReadFile("../../db/001_baseline.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(ctx, string(sql), pgx.QueryExecModeSimpleProtocol); e != nil {
		t.Fatal(e)
	}
	// Verify migration default and preservation against a preexisting baseline row.
	var legacyID, before string
	if e = db.QueryRow(ctx, "INSERT INTO pulso_iot.devices(name,device_identifier,client_id,mqtt_username) VALUES('Legacy','dev-legacy','dev-legacy','dev-legacy') RETURNING id::text").Scan(&legacyID); e != nil {
		t.Fatal(e)
	}
	if e = db.QueryRow(ctx, "SELECT row_to_json(d)::text FROM pulso_iot.devices d WHERE id=$1", legacyID).Scan(&before); e != nil {
		t.Fatal(e)
	}
	migration, err := os.ReadFile("../../db/002_device_mode.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, string(migration), pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
	var unchanged bool
	if e = db.QueryRow(ctx, "SELECT device_mode='physical' AND (to_jsonb(d)-'device_mode')=$2::jsonb FROM pulso_iot.devices d WHERE id=$1", legacyID, before).Scan(&unchanged); e != nil || !unchanged {
		t.Fatal("migration changed legacy record", e)
	}
	if _, e = db.Exec(ctx, "DELETE FROM pulso_iot.devices WHERE id=$1", legacyID); e != nil {
		t.Fatal(e)
	}
	f := &fakeBroker{users: map[string]string{}, online: map[string]bool{}}
	s := httptest.NewServer(http.HandlerFunc(f.handler))
	c := Config{DatabaseURL: u, Key: bytes.Repeat([]byte{1}, 32), JWTSecret: strings.Repeat("z", 32), TokenTTL: 15 * time.Minute, CommandTimeout: 100 * time.Millisecond, MaxDevices: 10, MaxMessages: 1000, MQTTBroker: "mqtts://localhost:8883", MQTTUser: "svc-backend", MQTTClientID: "svc-backend", MQTTPassword: "test-placeholder"}
	a := New(ctx, c, db)
	a.Broker = emqx.New(s.URL, "k", "s")
	a.MQTT = &fakePublisher{a: a}
	tx, e := db.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = Bootstrap(ctx, tx, "Admin", "admin@example.com", "Admin-pass!1234"); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		a.Close()
		s.Close()
		_, e := db.Exec(ctx, "DROP SCHEMA pulso_iot CASCADE", pgx.QueryExecModeSimpleProtocol)
		if e != nil {
			t.Error(e)
		}
		db.Close()
	})
	return a, f
}

type fakePublisher struct {
	a       *App
	respond bool
}

func (p *fakePublisher) Ready() bool { return true }
func (p *fakePublisher) Publish(ctx context.Context, topic string, b []byte, q byte) error {
	if p.respond {
		var v map[string]any
		json.Unmarshal(b, &v)
		payload, _ := json.Marshal(map[string]any{"command_id": v["command_id"], "status": "executed", "timestamp": time.Now().UTC().Format(time.RFC3339Nano)})
		return p.a.Ingest(ctx, Incoming{strings.TrimSuffix(topic, "commands") + "command-responses", payload, q, false})
	}
	return nil
}
func req(t *testing.T, h http.Handler, method, path, token string, body any, want int) map[string]any {
	t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(b))
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s status %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	var v map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(w.Body.String(), "password_ciphertext") || strings.Contains(w.Body.String(), "password_hash") {
		t.Fatal("secret in response")
	}
	return v
}
func loginToken(t *testing.T, h http.Handler, email, password string) string {
	return req(t, h, "POST", "/auth/login", "", map[string]any{"email": email, "password": password}, 200)["data"].(map[string]any)["access_token"].(string)
}
func createTestDevice(t *testing.T, h http.Handler, token, name string) string {
	v := req(t, h, "POST", "/devices", token, map[string]any{"name": "Sensor", "device_identifier": name, "device_mode": "simulated"}, 201)["data"].(map[string]any)
	if len(v["mqtt_password_once"].(string)) < 32 {
		t.Fatal("generated password")
	}
	return v["id"].(string)
}
func TestPlatformPostgres(t *testing.T) {
	a, f := fixture(t)
	h := a.Handler()
	ctx := context.Background()
	req(t, h, "POST", "/auth/login", "", map[string]string{"email": "admin@example.com", "password": "wrong"}, 401)
	token := loginToken(t, h, "admin@example.com", "Admin-pass!1234")
	req(t, h, "GET", "/auth/me", token, nil, 200)
	req(t, h, "GET", "/devices", "", nil, 401)
	roles := map[string]string{}
	users := map[string]string{}
	for _, role := range []string{"operator", "viewer"} {
		v := req(t, h, "POST", "/users", token, map[string]any{"name": role, "email": role + "@example.com", "password": "Strong-pass!1234", "role": role}, 201)["data"].(map[string]any)
		users[role] = v["id"].(string)
		roles[role] = loginToken(t, h, role+"@example.com", "Strong-pass!1234")
	}
	req(t, h, "GET", "/users", roles["operator"], nil, 403)
	req(t, h, "POST", "/devices", roles["viewer"], map[string]any{}, 403)
	req(t, h, "POST", "/users", token, map[string]any{"name": "Duplicate", "email": "VIEWER@example.com", "password": "Strong-pass!1234"}, 409)
	me := req(t, h, "GET", "/auth/me", token, nil, 200)["data"].(map[string]any)
	req(t, h, "DELETE", "/users/"+me["id"].(string), token, nil, 409)
	req(t, h, "PATCH", "/users/"+users["viewer"]+"/password", token, map[string]string{"password": "Another-pass!1234"}, 200)
	req(t, h, "PATCH", "/users/"+users["viewer"], token, map[string]string{"name": "Viewer edit"}, 200)
	req(t, h, "GET", "/users?page=1&limit=1&search=Viewer", token, nil, 200)
	id := createTestDevice(t, h, token, "dev-001")
	id2 := createTestDevice(t, h, roles["operator"], "dev-002")
	req(t, h, "GET", "/devices/"+id, roles["viewer"], nil, 200)
	req(t, h, "GET", "/devices/"+id+"/topics", token, nil, 200)
	req(t, h, "PATCH", "/devices/"+id, token, map[string]string{"device_identifier": "hijacked"}, 400)
	req(t, h, "GET", "/devices?page=1&limit=1&sort=name", token, nil, 200)
	req(t, h, "GET", "/devices?limit=0", token, nil, 400)
	req(t, h, "DELETE", "/devices/"+id2, roles["operator"], nil, 403)
	var cipher []byte
	if e := a.DB.QueryRow(ctx, "SELECT password_ciphertext FROM pulso_iot.mqtt_credentials WHERE device_id=$1", id).Scan(&cipher); e != nil {
		t.Fatal(e)
	}
	secret, e := Open(a.C.Key, id, cipher)
	if e != nil || f.users["dev-001"] != secret {
		t.Fatal("credential mismatch")
	}
	req(t, h, "POST", "/devices/"+id+"/credentials/rotate", token, map[string]any{}, 200)
	var version int
	a.DB.QueryRow(ctx, "SELECT credential_version FROM pulso_iot.mqtt_credentials WHERE device_id=$1", id).Scan(&version)
	if version != 2 {
		t.Fatal(version)
	}
	payload := []byte(`{"message_id":"same-id","timestamp":"2026-10-31T12:00:00Z","temperature_c":26,"humidity_pct":60}`)
	for i := 0; i < 2; i++ {
		if e = a.Ingest(ctx, Incoming{Prefix + "dev-001/telemetry", payload, 1, false}); e != nil {
			t.Fatal(e)
		}
	}
	var n int
	a.DB.QueryRow(ctx, "SELECT count(*) FROM pulso_iot.mqtt_messages WHERE device_id=$1", id).Scan(&n)
	if n != 1 {
		t.Fatal("dedup", n)
	}
	req(t, h, "GET", "/devices/"+id+"/telemetry", token, nil, 200)
	req(t, h, "GET", "/messages?date_from=invalid", token, nil, 400)
	a.MQTT = &fakePublisher{a: a, respond: true}
	v := req(t, h, "POST", "/devices/"+id+"/commands", roles["operator"], map[string]string{"type": "ping"}, 201)["data"].(map[string]any)
	if v["status"] != "executed" {
		t.Fatal("fast response overwritten", v)
	}
	req(t, h, "GET", "/commands/"+v["id"].(string), token, nil, 200)
	req(t, h, "POST", "/devices/"+id+"/commands", token, map[string]string{"type": "shell"}, 422)
	a.MQTT = &fakePublisher{a: a}
	v = req(t, h, "POST", "/devices/"+id+"/commands", token, map[string]string{"type": "ping"}, 201)["data"].(map[string]any)
	time.Sleep(120 * time.Millisecond)
	if e = a.ExpireCommands(); e != nil {
		t.Fatal(e)
	}
	var status string
	a.DB.QueryRow(ctx, "SELECT status FROM pulso_iot.device_commands WHERE id=$1", v["id"]).Scan(&status)
	if status != "timeout" {
		t.Fatal(status)
	}

	// Fail closed on an existing broker identity and allow only explicit recovery.
	f.mu.Lock()
	f.users["dev-existing"] = "existing-test-secret"
	f.mu.Unlock()
	existing := req(t, h, "POST", "/devices", token, map[string]any{"name": "Existing", "device_identifier": "dev-existing"}, 503)["data"].(map[string]any)
	f.mu.Lock()
	unchanged := f.users["dev-existing"] == "existing-test-secret"
	f.mu.Unlock()
	if !unchanged {
		t.Fatal("silently replaced broker credential")
	}
	req(t, h, "POST", "/devices/"+existing["id"].(string)+"/credentials/rotate", token, map[string]any{}, 200)
	// Saving intent before an outage makes creation recoverable without another POST.
	f.mu.Lock()
	f.fail = true
	f.mu.Unlock()
	pending := req(t, h, "POST", "/devices", token, map[string]any{"name": "Recover", "device_identifier": "dev-recover"}, 503)["data"].(map[string]any)
	f.mu.Lock()
	f.fail = false
	f.mu.Unlock()
	req(t, h, "POST", "/devices/"+pending["id"].(string)+"/reconcile", token, nil, 200)
	f.mu.Lock()
	f.online["dev-002"] = true
	f.mu.Unlock()
	req(t, h, "POST", "/tests", token, map[string]any{"device_ids": []string{"dev-002"}, "messages": 2, "interval_ms": 100, "qos": 1}, 409)
	f.mu.Lock()
	f.online["dev-002"] = false
	f.mu.Unlock()
	// Runner injection validates lifecycle and real counters without a network dependency.
	a.Factory = func(c simconfig.Config, d simconfig.Device) runner.Transport { return &fakeTransport{a: a, d: d} }
	testBody := map[string]any{"device_ids": []string{"dev-001", "dev-002"}, "messages": 2, "interval_ms": 100, "qos": 1}
	v = req(t, h, "POST", "/tests", token, testBody, 202)["data"].(map[string]any)
	runID := v["id"].(string)
	waitTest(t, a, runID, "completed")
	detail := req(t, h, "GET", "/tests/"+runID, roles["viewer"], nil, 200)["data"].(map[string]any)
	results := detail["results"].([]any)
	if len(results) != 2 {
		t.Fatal(detail)
	}
	for _, r := range results {
		row := r.(map[string]any)
		if row["published"] != float64(2) || row["acknowledged_by_broker"] != float64(2) || row["received_by_backend"] != nil || row["avg_latency_ms"] != nil {
			t.Fatal(row)
		}
	}
	testBody["messages"] = 20
	testBody["interval_ms"] = 1000
	v = req(t, h, "POST", "/tests", token, testBody, 202)["data"].(map[string]any)
	cancelID := v["id"].(string)
	req(t, h, "POST", "/tests", token, testBody, 409)
	req(t, h, "PATCH", "/devices/"+id, token, map[string]string{"name": "Busy"}, 409)
	req(t, h, "POST", "/tests/"+cancelID+"/cancel", token, nil, 202)
	waitTest(t, a, cancelID, "cancelled")
	_, e = a.DB.Exec(ctx, "UPDATE pulso_iot.mqtt_credentials SET password_ciphertext=NULL,password_saved_at=NULL WHERE device_id=$1", id2)
	if e != nil {
		t.Fatal(e)
	}
	req(t, h, "POST", "/tests", token, testBody, 422)
	req(t, h, "PATCH", "/devices/"+id, token, map[string]string{"status": "inactive"}, 200)
	if _, ok := f.users["dev-001"]; ok {
		t.Fatal("inactive broker user")
	}
	req(t, h, "PATCH", "/devices/"+id, token, map[string]string{"status": "active"}, 200)
	f.mu.Lock()
	f.fail = true
	f.mu.Unlock()
	req(t, h, "DELETE", "/devices/"+id, token, nil, 503)
	var ps string
	a.DB.QueryRow(ctx, "SELECT provisioning_status FROM pulso_iot.mqtt_credentials WHERE device_id=$1", id).Scan(&ps)
	if ps != "error" {
		t.Fatal(ps)
	}
	f.mu.Lock()
	f.fail = false
	f.mu.Unlock()
	req(t, h, "POST", "/devices/"+id+"/reconcile", token, nil, 200)
	req(t, h, "GET", "/devices/"+id, token, nil, 404)
	for _, path := range []string{"/dashboard/summary", "/dashboard/message-volume?period=24h", "/dashboard/recent-messages", "/tests", "/audit-logs"} {
		req(t, h, "GET", path, token, nil, 200)
	}
	req(t, h, "GET", "/audit-logs", roles["viewer"], nil, 403)
	req(t, h, "DELETE", "/users/"+users["viewer"], token, nil, 200)
	req(t, h, "GET", "/auth/me", roles["viewer"], nil, 401)
}

type fakeTransport struct {
	a *App
	d simconfig.Device
}

func (f *fakeTransport) Start(ctx context.Context, h func([]byte), n func(runner.Notice)) error {
	n(runner.Notice{Kind: "connected"})
	return nil
}
func (f *fakeTransport) Publish(ctx context.Context, topic string, b []byte, q byte) (runner.Receipt, error) {
	e := f.a.Ingest(ctx, Incoming{topic, b, q, false})
	rc := byte(0)
	return runner.Receipt{Acknowledged: q > 0, ReasonCode: &rc}, e
}
func (f *fakeTransport) Close(context.Context) error { return nil }
func waitTest(t *testing.T, a *App, id, want string) {
	t.Helper()
	end := time.Now().Add(15 * time.Second)
	for time.Now().Before(end) {
		var status string
		var summary []byte
		e := a.DB.QueryRow(context.Background(), "SELECT status,summary FROM pulso_iot.test_runs WHERE id=$1", id).Scan(&status, &summary)
		if e != nil {
			t.Fatal(e)
		}
		if status == want {
			for {
				a.mu.Lock()
				_, busy := a.active[id]
				a.mu.Unlock()
				if !busy {
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
		if status == "failed" || status == "completed" || status == "cancelled" {
			t.Fatalf("test status %s want %s: %s", status, want, summary)
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal(fmt.Sprintf("test timeout %s", id))
}

package app

import (
	"bytes"

	"github.com/google/uuid"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCredentialEnvelope(t *testing.T) {
	key := bytes.Repeat([]byte{8}, 32)
	id := uuid.NewString()
	p := "segredo-Teste!12345"
	b, e := Seal(key, id, p)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(b, []byte(p)) || len(b) != 12+len(p)+16 {
		t.Fatal("formato incorreto")
	}
	s, e := Open(key, id, b)
	if e != nil || s != p {
		t.Fatal("roundtrip")
	}
	for _, v := range []struct {
		k  []byte
		id string
		b  []byte
	}{{bytes.Repeat([]byte{9}, 32), id, b}, {key, uuid.NewString(), b}, {key, id, b[:12]}} {
		if _, e = Open(v.k, v.id, v.b); e == nil {
			t.Fatal("aceitou chave/AAD/envelope incorreto")
		}
	}
	b[len(b)-1] ^= 1
	if _, e = Open(key, id, b); e == nil {
		t.Fatal("aceitou corrupção")
	}
}
func TestRBAC(t *testing.T) {
	for _, role := range []string{"admin", "operator", "viewer", "invalid"} {
		for _, p := range []string{"admin", "write", "read"} {
			want := role == "admin" || role == "operator" && p != "admin" || role == "viewer" && p == "read"
			if allowed(role, p) != want {
				t.Fatalf("%s %s", role, p)
			}
		}
	}
}
func TestTestValidation(t *testing.T) {
	c := Config{MaxDevices: 10, MaxMessages: 1000}
	good := testBody{[]string{"dev-001", "dev-002"}, 20, 1000, 1}
	if e := c.validateTest(good); e != nil {
		t.Fatal(e)
	}
	bad := []testBody{{[]string{"dev-001", "dev-001"}, 20, 1000, 1}, {[]string{"invalid/id"}, 20, 1000, 1}, {[]string{"dev-001"}, 0, 1000, 1}, {[]string{"dev-001"}, 20, 0, 1}, {[]string{"dev-001"}, 20, 1000, 3}, {[]string{"dev-001"}, 1000, 60000, 1}}
	for _, v := range bad {
		if c.validateTest(v) == nil {
			t.Fatal(v)
		}
	}
}
func TestIncomingValidation(t *testing.T) {
	base := Incoming{Prefix + "dev-001/telemetry", []byte(`{"message_id":"m1","timestamp":"2026-10-31T12:00:00Z","temperature_c":25,"humidity_pct":60}`), 1, false}
	if _, _, _, ok := validateIncoming(base); !ok {
		t.Fatal("valid telemetry")
	}
	for _, v := range []Incoming{{base.Topic, base.Payload, 1, true}, {Prefix + "dev-002/commands", base.Payload, 1, false}, {base.Topic, []byte(`{"temperature_c":25}`), 1, false}, {base.Topic, []byte(`null`), 1, false}, {base.Topic, []byte(`{"device_id":"dev-002","timestamp":"2026-10-31T12:00:00Z","temperature_c":25,"humidity_pct":60}`), 1, false}} {
		if _, _, _, ok := validateIncoming(v); ok {
			t.Fatal("accepted invalid")
		}
	}
}
func TestStrictJSON(t *testing.T) {
	for _, body := range []string{`{"mqtt_password":"x","unexpected":1}`, `{} {}`, `null`} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		var v struct {
			Password string `json:"mqtt_password"`
		}
		e := decode(httptest.NewRecorder(), r, &v)
		if e == nil {
			t.Fatal(body)
		}
	}
}

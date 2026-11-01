package device

import (
	"testing"
	"time"
)

func TestTopics(t *testing.T) {
	v := TopicsFor("dev-001")
	if v.Telemetry != "desbravatech/v1/devices/dev-001/telemetry" || v.Commands != "desbravatech/v1/devices/dev-001/commands" || v.Responses != "desbravatech/v1/devices/dev-001/command-responses" {
		t.Fatal(v)
	}
}
func TestCommands(t *testing.T) {
	now := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	cases := []struct {
		name, payload, status string
		bad, noResponse       bool
	}{
		{"ping", `{"command_id":"cmd-001","type":"ping","issued_at":"2026-10-28T01:00:00Z"}`, "executed", false, false},
		{"alias", `{"command_id":"cmd-001","action":"ping"}`, "executed", false, false},
		{"unsupported", `{"command_id":"cmd-001","type":"reboot"}`, "unsupported", true, false},
		{"invalid timestamp", `{"command_id":"cmd-001","type":"ping","issued_at":"x"}`, "invalid", true, false},
		{"conflict", `{"command_id":"cmd-001","type":"ping","action":"reboot"}`, "invalid", true, false},
		{"missing type", `{"command_id":"cmd-001"}`, "invalid", true, false},
		{"no id", `{"type":"ping"}`, "", true, true},
		{"bad json", `{`, "", true, true},
		{"bad field", `{"command_id":2,"type":"ping"}`, "", true, true},
		{"null", `null`, "", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, e := Process([]byte(c.payload), now)
			if (e != nil) != c.bad {
				t.Fatalf("error=%v", e)
			}
			if c.noResponse {
				if r != nil {
					t.Fatal(r)
				}
				return
			}
			if r == nil || r.Status != c.status || r.CommandID != "cmd-001" || r.Timestamp != now.Format(time.RFC3339Nano) {
				t.Fatalf("response=%+v", r)
			}
		})
	}
	if r, e := Process(make([]byte, 65537), now); r != nil || e == nil {
		t.Fatal("large command accepted")
	}
}

package app

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeviceModeContract(t *testing.T) {
	for _, raw := range []string{`null`, `""`, `123`, `true`, `{}`, `[]`, `"virtual"`, `"Physical"`} {
		mode, err := parseDeviceMode(json.RawMessage(raw))
		p, ok := err.(*Problem)
		if mode != nil || !ok || p.Status != 422 || p.Code != "INVALID_DEVICE_MODE" {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	for _, value := range []string{"physical", "simulated"} {
		var b deviceBody
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{"device_mode":"`+value+`"}`))
		r.Header.Set("Content-Type", "application/json")
		if err := decode(httptest.NewRecorder(), r, &b); err != nil {
			t.Fatal(err)
		}
		mode, err := parseDeviceMode(b.Mode)
		if err != nil || mode == nil || *mode != value {
			t.Fatal(value, err)
		}
	}
	mode, err := parseDeviceMode(nil)
	if mode != nil || err != nil {
		t.Fatal("omission must remain distinguishable")
	}
	c := Config{MaxDevices: 10, MaxMessages: 1000}
	if err := c.validateTest(testBody{[]string{"sensor-001"}, 1, 100, 1}); err != nil {
		t.Fatal(err)
	}
}

//go:build integration

package app

import (
	"context"
	simconfig "github.com/woicie/iot-simulator/pkg/config"
	"github.com/woicie/iot-simulator/pkg/runner"
	"testing"
)

func TestDeviceModePostgres(t *testing.T) {
	a, f := fixture(t)
	h := a.Handler()
	token := loginToken(t, h, "admin@example.com", "Admin-pass!1234")
	ctx := context.Background()
	physical := req(t, h, "POST", "/devices", token, map[string]any{"name": "Hardware", "device_identifier": "dev-physical"}, 201)["data"].(map[string]any)
	id := physical["id"].(string)
	if physical["device_mode"] != "physical" {
		t.Fatal(physical)
	}
	simID := createTestDevice(t, h, token, "sensor-001")
	checkMode := func(want string) {
		t.Helper()
		v := req(t, h, "GET", "/devices/"+id, token, nil, 200)["data"].(map[string]any)
		if v["device_mode"] != want {
			t.Fatal(v)
		}
	}
	for _, bad := range []any{nil, "", 123, true, []string{}, "virtual"} {
		for _, method := range []string{"POST", "PATCH"} {
			path := "/devices"
			if method == "PATCH" {
				path += "/" + id
			}
			v := req(t, h, method, path, token, map[string]any{"device_mode": bad}, 422)
			if v["error"].(map[string]any)["code"] != "INVALID_DEVICE_MODE" {
				t.Fatal(v)
			}
		}
	}
	req(t, h, "PATCH", "/devices/"+id, token, map[string]string{"name": "Edited"}, 200)
	checkMode("physical")
	req(t, h, "GET", "/devices?device_mode=virtual", token, nil, 400)
	req(t, h, "GET", "/devices?device_mode=", token, nil, 400)
	v := req(t, h, "GET", "/devices?device_mode=simulated&page=1&limit=1", token, nil, 200)
	rows := v["data"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["device_mode"] != "simulated" {
		t.Fatal(v)
	}
	f.mu.Lock()
	f.online["dev-physical"] = true
	f.mu.Unlock()
	req(t, h, "PATCH", "/devices/"+id, token, map[string]string{"device_mode": "physical"}, 200)
	req(t, h, "PATCH", "/devices/"+id, token, map[string]string{"device_mode": "simulated"}, 409)
	checkMode("physical")
	f.mu.Lock()
	f.online["dev-physical"] = false
	f.fail = true
	f.mu.Unlock()
	req(t, h, "PATCH", "/devices/"+id, token, map[string]string{"device_mode": "simulated"}, 503)
	f.mu.Lock()
	f.fail = false
	f.mu.Unlock()
	checkMode("physical")
	a.mu.Lock()
	a.busy[id] = true
	a.mu.Unlock()
	req(t, h, "PATCH", "/devices/"+id, token, map[string]string{"device_mode": "simulated"}, 409)
	a.mu.Lock()
	delete(a.busy, id)
	a.mu.Unlock()
	var before int
	a.DB.QueryRow(ctx, "SELECT count(*) FROM pulso_iot.test_runs").Scan(&before)
	for _, ids := range [][]string{{"dev-physical"}, {"sensor-001", "dev-physical"}} {
		v := req(t, h, "POST", "/tests", token, map[string]any{"device_ids": ids, "messages": 1, "interval_ms": 100, "qos": 1}, 422)
		if v["error"].(map[string]any)["code"] != "DEVICE_NOT_SIMULATED" {
			t.Fatal(v)
		}
	}
	var after int
	a.DB.QueryRow(ctx, "SELECT count(*) FROM pulso_iot.test_runs").Scan(&after)
	if before != after || len(a.busy) != 0 || len(a.active) != 0 {
		t.Fatal("partial effects")
	}
	var cipher []byte
	var version int
	a.DB.QueryRow(ctx, "SELECT password_ciphertext,credential_version FROM pulso_iot.mqtt_credentials WHERE device_id=$1", id).Scan(&cipher, &version)
	req(t, h, "PATCH", "/devices/"+id, token, map[string]string{"device_mode": "simulated"}, 200)
	checkMode("simulated")
	var same bool
	if err := a.DB.QueryRow(ctx, "SELECT password_ciphertext=$2 AND credential_version=$3 FROM pulso_iot.mqtt_credentials WHERE device_id=$1", id, cipher, version).Scan(&same); err != nil || !same {
		t.Fatal("credentials changed", err)
	}
	a.Factory = func(c simconfig.Config, d simconfig.Device) runner.Transport { return &fakeTransport{a: a, d: d} }
	run := req(t, h, "POST", "/tests", token, map[string]any{"device_ids": []string{"sensor-001"}, "messages": 1, "interval_ms": 100, "qos": 1}, 202)["data"].(map[string]any)
	waitTest(t, a, run["id"].(string), "completed")
	_ = simID
}

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const example = `broker: mqtts://broker.woicie.com.br:8883
mqtt_version: "5.0"
qos: 1
interval: 5s
devices:
  - id: dev-001
    password: "${TEST_SECRET}"
    telemetry:
      temperature_c: {min: 20, max: 30}
      humidity_pct: {min: 40, max: 80}
  - id: dev-002
    password: example-placeholder
    qos: 0
    interval: 1s
    telemetry:
      temperature_c: {min: 21, max: 32}
      humidity_pct: {min: 45, max: 80}
`

func lookup(k string) (string, bool) { return "punctuation: # \"\n- password-value", true }
func TestLoadDefaultsAndOverrides(t *testing.T) {
	t.Setenv("TEST_SECRET", "local-test-placeholder")
	p := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(p, []byte(example), 0600)
	c, e := Load(p)
	if e != nil {
		t.Fatal(e)
	}
	if c.Devices[0].ClientID != "dev-001" || c.Devices[0].Username != "dev-001" || *c.Devices[0].QoS != 1 || c.Devices[0].Interval.Value() != 5*time.Second || *c.Devices[1].QoS != 0 || c.Devices[1].Interval.Value() != time.Second {
		t.Fatalf("defaults/overrides incorrect")
	}
}
func TestSecretExpansionDoesNotInjectYAML(t *testing.T) {
	c, e := Parse([]byte(example), lookup)
	if e != nil {
		t.Fatal(e)
	}
	want, _ := lookup("")
	if c.Devices[0].Password != want {
		t.Fatal("secret changed")
	}
	_, e = Parse([]byte(example), func(string) (string, bool) { return "", false })
	if e == nil || !strings.Contains(e.Error(), "TEST_SECRET") {
		t.Fatal("missing env accepted")
	}
	_, e = Parse([]byte(example), func(string) (string, bool) { return "", true })
	if e == nil {
		t.Fatal("empty env accepted")
	}
}
func TestRejectInvalidConfig(t *testing.T) {
	cases := map[string]string{
		"id":              strings.Replace(example, "id: dev-001", "id: dev/001", 1),
		"duplicate":       strings.Replace(example, "id: dev-002", "id: dev-001", 1),
		"client identity": strings.Replace(example, "id: dev-001", "id: dev-001\n    client_id: different", 1),
		"tls":             strings.Replace(example, "mqtts://", "mqtt://", 1),
		"port":            strings.Replace(example, ":8883", ":1883", 1),
		"url secret":      strings.Replace(example, "mqtts://", "mqtts://secret@", 1),
		"qos":             strings.Replace(example, "qos: 1", "qos: 3", 1),
		"interval":        strings.Replace(example, "interval: 5s", "interval: -5s", 1),
		"humidity":        strings.Replace(example, "max: 80", "max: 101", 1),
		"range":           strings.Replace(example, "min: 20, max: 30", "min: 31, max: 30", 1),
		"nan":             strings.Replace(example, "min: 20", "min: .nan", 1),
		"version":         strings.Replace(example, `"5.0"`, `"3.1.1"`, 1),
		"field":           example + "unknown_field: true\n",
		"documents":       example + "---\nhello: world\n",
		"type secret":     strings.Replace(example, "qos: 1", "qos: private-secret-value", 1),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, e := Parse([]byte(data), lookup)
			if e == nil {
				t.Fatal("invalid config accepted")
			}
			if strings.Contains(e.Error(), "private-secret-value") {
				t.Fatal("secret leaked")
			}
		})
	}
}
func TestIDs(t *testing.T) {
	for _, s := range []string{"dev-001", "a", "0", "dev-1-a"} {
		if !ValidID(s) {
			t.Fatal(s)
		}
	}
	for _, s := range []string{"", "Dev-001", "dev_1", "a+", "a#", "/", "$SYS", "a--b", "-a", "a-", " a"} {
		if ValidID(s) {
			t.Fatal(s)
		}
	}
}

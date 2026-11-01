package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/woicie/iot-simulator/pkg/runner"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	o, e := Parse([]string{"run", "--devices", "dev-001,dev-002", "--messages", "20", "--qos", "0", "--interval", "1s", "--grace-period", "0s", "--output-json", "out.json"}, io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	if o.Request.Messages != 20 || len(o.Request.DeviceIDs) != 2 || *o.Request.QoS != 0 || o.Request.Interval != time.Second || *o.Request.GracePeriod != 0 {
		t.Fatal(o)
	}
}
func TestParseErrors(t *testing.T) {
	for _, args := range [][]string{{"run", "--messages", "0"}, {"run", "--qos", "-1"}, {"--interval", "0s"}, {"--messages", "2"}, {"run", "--devices", "dev-001,"}, {"run", "--devices", ""}, {"--grace-period", "-1s"}, {"--timeout", "-1s"}, {"--unknown"}, {"run", "extra"}, {"--output-json", "config.yaml"}} {
		if _, e := Parse(args, io.Discard); e == nil {
			t.Fatal(args)
		}
	}
}
func TestHelpVersionAndConfigExit(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--version"}, {"run", "--help"}, {"run", "--version"}} {
		var out bytes.Buffer
		if Execute(context.Background(), args, &out, io.Discard) != 0 || out.Len() == 0 {
			t.Fatal(args)
		}
	}
	if Execute(context.Background(), []string{"--config", filepath.Join(t.TempDir(), "missing.yaml")}, io.Discard, io.Discard) != 2 {
		t.Fatal("exit")
	}
}
func TestWriteReport(t *testing.T) {
	p := filepath.Join(t.TempDir(), "results.json")
	want := runner.RunResult{RunID: "test-id", Status: "completed"}
	if e := WriteResult(p, want); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	var got runner.RunResult
	if json.Unmarshal(b, &got) != nil || got.RunID != want.RunID {
		t.Fatal(string(b))
	}
}

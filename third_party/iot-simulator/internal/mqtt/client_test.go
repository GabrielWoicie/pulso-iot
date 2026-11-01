package mqtt

import (
	"context"
	"github.com/woicie/iot-simulator/pkg/config"
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	b := Backoff(5 * time.Second)
	for i, want := range []time.Duration{0, time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second} {
		if got := b(i); got != want {
			t.Fatalf("attempt %d: %v", i, got)
		}
	}
	if b(10000) != 5*time.Second {
		t.Fatal("unbounded")
	}
}
func TestForbiddenTopicBeforeConnection(t *testing.T) {
	c := New(config.Defaults(), config.Device{ID: "dev-001"})
	if _, e := c.Publish(context.Background(), "desbravatech/v1/devices/dev-002/telemetry", nil, 1); e == nil {
		t.Fatal("cross-device publish permitted")
	}
}
func TestWaitReadyCancellation(t *testing.T) {
	c := New(config.Defaults(), config.Device{ID: "dev-001"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c.WaitReady(ctx) == nil {
		t.Fatal("not cancelled")
	}
}

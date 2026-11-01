package telemetry

import (
	"github.com/woicie/iot-simulator/pkg/config"
	"math"
	"testing"
	"time"
)

func TestBoundsSmoothnessReproducibilityAndIDs(t *testing.T) {
	r := config.Sensors{Temperature: config.Range{Min: 20.01, Max: 30.03}, Humidity: config.Range{Min: 40, Max: 75}}
	a, b := New(r, 42), New(r, 42)
	seen := map[string]bool{}
	prev := a.Next(time.Now())
	b.Next(time.Now())
	for i := 0; i < 10000; i++ {
		m, n := a.Next(time.Now()), b.Next(time.Now())
		if m.Temperature != n.Temperature || m.Humidity != n.Humidity {
			t.Fatal("seed mismatch")
		}
		if m.Temperature < r.Temperature.Min || m.Temperature > r.Temperature.Max || m.Humidity < 40 || m.Humidity > 75 {
			t.Fatal("out of bounds")
		}
		if math.Abs(m.Temperature-prev.Temperature) > 0.251 || math.Abs(m.Humidity-prev.Humidity) > 0.876 {
			t.Fatal("abrupt variation")
		}
		if seen[m.ID] {
			t.Fatal("duplicate message id")
		}
		seen[m.ID] = true
		if _, e := time.Parse(time.RFC3339Nano, m.Timestamp); e != nil {
			t.Fatal(e)
		}
		prev = m
	}
}

package telemetry

import (
	"github.com/woicie/iot-simulator/internal/identity"
	"github.com/woicie/iot-simulator/pkg/config"
	"math"
	"math/rand"
	"time"
)

type Message struct {
	ID          string  `json:"message_id"`
	Timestamp   string  `json:"timestamp"`
	Temperature float64 `json:"temperature_c"`
	Humidity    float64 `json:"humidity_pct"`
}

// Generator is owned by one device goroutine. Fixed seeds reproduce sensor values, not UUIDs/time.
type Generator struct {
	rng    *rand.Rand
	ranges config.Sensors
	t, h   float64
}

func New(r config.Sensors, seed int64) *Generator {
	return &Generator{rand.New(rand.NewSource(seed)), r, (r.Temperature.Min + r.Temperature.Max) / 2, (r.Humidity.Min + r.Humidity.Max) / 2}
}
func (g *Generator) step(v float64, r config.Range) float64 {
	v += (g.rng.Float64()*2 - 1) * (r.Max - r.Min) * 0.025
	v = math.Max(r.Min, math.Min(r.Max, v))
	return v
}
func (g *Generator) Next(now time.Time) Message {
	g.t = g.step(g.t, g.ranges.Temperature)
	g.h = g.step(g.h, g.ranges.Humidity)
	return Message{identity.New(), now.UTC().Format(time.RFC3339Nano), g.t, g.h}
}

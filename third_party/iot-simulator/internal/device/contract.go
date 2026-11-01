package device

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const Prefix = "desbravatech/v1/devices/"

type Topics struct{ Telemetry, Commands, Responses string }

func TopicsFor(id string) Topics {
	p := Prefix + id
	return Topics{p + "/telemetry", p + "/commands", p + "/command-responses"}
}

type Command struct {
	ID       string `json:"command_id"`
	Type     string `json:"type"`
	Action   string `json:"action,omitempty"`
	IssuedAt string `json:"issued_at,omitempty"`
}
type Response struct {
	CommandID string `json:"command_id"`
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
}

// Process never executes OS commands. Invalid uncorrelatable payloads get no response.
func Process(payload []byte, now time.Time) (*Response, error) {
	if len(payload) > 65536 {
		return nil, errors.New("comando excede 64 KiB")
	}
	var c Command
	if e := json.Unmarshal(payload, &c); e != nil {
		return nil, errors.New("JSON de comando inválido")
	}
	if strings.TrimSpace(c.ID) == "" || len(c.ID) > 128 || strings.ContainsAny(c.ID, "\r\n\x00") {
		return nil, errors.New("command_id ausente ou inválido")
	}
	r := &Response{CommandID: c.ID, Timestamp: now.UTC().Format(time.RFC3339Nano), Status: "invalid"}
	if c.Type == "" {
		c.Type = c.Action
	} else if c.Action != "" && c.Action != c.Type {
		return r, errors.New("type e action conflitantes")
	}
	if c.Type == "" {
		return r, errors.New("type obrigatório")
	}
	if c.IssuedAt != "" {
		if _, e := time.Parse(time.RFC3339Nano, c.IssuedAt); e != nil {
			return r, errors.New("issued_at inválido")
		}
	}
	if c.Type != "ping" {
		r.Status = "unsupported"
		return r, errors.New("tipo de comando não suportado")
	}
	r.Status = "executed"
	return r, nil
}

package app

import (
	"net/http"
	"time"
)

func (a *App) summary(w http.ResponseWriter, r *http.Request) error {
	v, e := oneJSON(r.Context(), a.DB, `SELECT (SELECT count(*) FROM pulso_iot.devices WHERE deleted_at IS NULL) AS devices_total,(SELECT count(*) FROM pulso_iot.devices WHERE deleted_at IS NULL AND status='active') AS devices_active,(SELECT count(*) FROM pulso_iot.users WHERE status='active') AS users_active,(SELECT count(*) FROM pulso_iot.mqtt_messages WHERE direction='inbound' AND received_at>=now()-interval '24 hours') AS messages_last_24h,(SELECT count(*) FROM pulso_iot.test_runs) AS tests_total`)
	if e != nil {
		return e
	}
	var clients struct {
		Meta struct {
			Count int `json:"count"`
		} `json:"meta"`
	}
	v["broker_connected_clients"] = nil
	if e = a.Broker.Call(r.Context(), "GET", "/clients?page=1&limit=1&connected=true", nil, &clients); e == nil {
		v["broker_connected_clients"] = clients.Meta.Count
	}
	v["broker_connected_clients_scope"] = "todas as sessões do broker, incluindo svc-backend"
	commands, e := queryJSON(r.Context(), a.DB, "SELECT id,device_id,command_id,status,issued_at,responded_at FROM pulso_iot.device_commands ORDER BY issued_at DESC LIMIT 5")
	if e != nil {
		return e
	}
	v["recent_commands"] = commands
	v["period"] = "24h"
	data(w, 200, v)
	return nil
}
func (a *App) volume(w http.ResponseWriter, r *http.Request) error {
	p := r.URL.Query().Get("period")
	if p == "" {
		p = "24h"
	}
	hours := 24
	switch p {
	case "1h":
		hours = 1
	case "24h":
	case "7d":
		hours = 168
	default:
		return problem(400, "INVALID_PERIOD", "Use 1h, 24h ou 7d")
	}
	v, e := queryJSON(r.Context(), a.DB, `SELECT buckets.bucket, count(m.id) AS messages FROM generate_series(date_trunc('hour',now()-($1::int-1)*interval '1 hour'),date_trunc('hour',now()),interval '1 hour') AS buckets(bucket) LEFT JOIN pulso_iot.mqtt_messages m ON m.received_at>=buckets.bucket AND m.received_at<buckets.bucket+interval '1 hour' AND m.direction='inbound' GROUP BY buckets.bucket ORDER BY buckets.bucket`, hours)
	if e == nil {
		data(w, 200, v)
	}
	return e
}
func (a *App) auditLogs(w http.ResponseWriter, r *http.Request) error {
	q, args := filters(r, map[string]string{"action": "action", "entity_type": "entity_type"})
	return a.list(w, r, "SELECT * FROM pulso_iot.audit_logs", q, "occurred_at DESC,id DESC", args)
}
func (a *App) brokerStatus(w http.ResponseWriter, r *http.Request) error {
	e := a.Broker.Preflight(r.Context())
	if e != nil {
		return problem(503, "BROKER_UNAVAILABLE", "Broker indisponível ou configuração incompatível")
	}
	data(w, 200, map[string]any{"api": "ready", "mqtt": a.MQTT != nil && a.MQTT.Ready(), "expected_version": "5.8.9"})
	return nil
}
func (a *App) health(w http.ResponseWriter, r *http.Request) error {
	db := a.DB.Ping(r.Context()) == nil
	mqtt := a.MQTT != nil && a.MQTT.Ready()
	status := 200
	if !db || !mqtt {
		status = 503
	}
	data(w, status, map[string]any{"database": db, "mqtt": mqtt, "time": time.Now().UTC(), "messages_persisted": a.Metrics.Received.Load(), "messages_dropped": a.Metrics.Dropped.Load(), "messages_invalid": a.Metrics.Invalid.Load(), "persistence_failures": a.Metrics.PersistenceFailed.Load()})
	return nil
}

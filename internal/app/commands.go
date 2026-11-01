package app

import (
	"encoding/json"
	"github.com/google/uuid"

	"net/http"
	"time"
)

func (a *App) sendCommand(w http.ResponseWriter, r *http.Request) error {
	id, e := requireID(r)
	if e != nil {
		return e
	}
	var b struct {
		Type   string `json:"type"`
		Action string `json:"action"`
	}
	if e = decode(w, r, &b); e != nil {
		return e
	}
	if b.Type == "" {
		b.Type = b.Action
	}
	if b.Type != "ping" || b.Action != "" && b.Action != b.Type {
		return problem(422, "UNSUPPORTED_COMMAND", "Somente ping é permitido")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopping {
		return problem(503, "SHUTTING_DOWN", "Serviço encerrando")
	}
	if a.MQTT == nil || !a.MQTT.Ready() {
		return problem(503, "MQTT_UNAVAILABLE", "Conexão MQTT indisponível")
	}
	var client string
	var qos int
	e = a.DB.QueryRow(r.Context(), `SELECT d.client_id,d.default_qos FROM pulso_iot.devices d JOIN pulso_iot.mqtt_credentials c ON c.device_id=d.id WHERE d.id=$1 AND d.status='active' AND d.deleted_at IS NULL AND c.provisioning_status='active'`, id).Scan(&client, &qos)
	if e != nil {
		return e
	}
	cid := uuid.NewString()
	payload, _ := json.Marshal(map[string]any{"command_id": cid, "type": "ping", "issued_at": time.Now().UTC().Format(time.RFC3339Nano)})
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	var record string
	e = tx.QueryRow(r.Context(), `INSERT INTO pulso_iot.device_commands(device_id,command_id,request_payload,created_by) VALUES($1,$2,$3,$4) RETURNING id::text`, id, cid, json.RawMessage(payload), actor(r)).Scan(&record)
	if e != nil {
		return e
	}
	if e = audit(r.Context(), tx, actor(r), "command.issued", "device_commands", record); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	if e = a.MQTT.Publish(r.Context(), Prefix+client+"/commands", payload, byte(qos)); e != nil {
		_, saveErr := a.DB.Exec(r.Context(), "UPDATE pulso_iot.device_commands SET status='failed',error_message='MQTT_PUBLISH_FAILED: entrega indeterminada' WHERE id=$1 AND status='pending'", record)
		if saveErr != nil {
			return saveErr
		}
		return problem(503, "MQTT_PUBLISH_FAILED", "Publicação falhou ou expirou; consulte histórico antes de reenviar")
	}
	tx, e = a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	_, e = tx.Exec(r.Context(), `UPDATE pulso_iot.device_commands SET status=CASE WHEN status='pending' THEN 'published' ELSE status END,published_at=now() WHERE id=$1`, record)
	if e != nil {
		return e
	}
	_, e = tx.Exec(r.Context(), `INSERT INTO pulso_iot.mqtt_messages(device_id,topic,message_kind,direction,command_id,payload,qos) VALUES($1,$2,'commands','outbound',$3,$4,$5)`, id, Prefix+client+"/commands", cid, json.RawMessage(payload), qos)
	if e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	v, e := oneJSON(r.Context(), a.DB, "SELECT * FROM pulso_iot.device_commands WHERE id=$1", record)
	if e == nil {
		data(w, 201, v)
	}
	return e
}
func (a *App) commands(w http.ResponseWriter, r *http.Request) error {
	id, e := requireID(r)
	if e != nil {
		return e
	}
	return a.list(w, r, "SELECT * FROM pulso_iot.device_commands", "device_id=$1", "issued_at DESC,id", []any{id})
}
func (a *App) command(w http.ResponseWriter, r *http.Request) error {
	id, e := requireID(r)
	if e != nil {
		return e
	}
	v, e := oneJSON(r.Context(), a.DB, "SELECT * FROM pulso_iot.device_commands WHERE id=$1", id)
	if e == nil {
		data(w, 200, v)
	}
	return e
}
func (a *App) messages(w http.ResponseWriter, r *http.Request) error {
	where, args := filters(r, map[string]string{"kind": "message_kind"})
	id := r.PathValue("id")
	if id != "" {
		u, e := uuid.Parse(id)
		if e != nil {
			return problem(400, "INVALID_ID", "ID deve ser UUID")
		}
		id = u.String()
	} else {
		id = r.URL.Query().Get("device_id")
		if id != "" {
			u, e := uuid.Parse(id)
			if e != nil {
				return problem(400, "INVALID_ID", "device_id deve ser UUID")
			}
			id = u.String()
		}
	}
	if id != "" {
		where += " AND device_id=" + arg(&args, id)
	}
	if r.Pattern == "GET /api/v1/devices/{id}/telemetry" {
		where += " AND message_kind='telemetry'"
	}
	for _, k := range []string{"date_from", "date_to"} {
		if s := r.URL.Query().Get(k); s != "" {
			t, e := time.Parse(time.RFC3339Nano, s)
			if e != nil {
				return problem(400, "INVALID_DATE", "Datas exigem RFC3339 com fuso")
			}
			op := ">="
			if k == "date_to" {
				op = "<="
			}
			where += " AND received_at " + op + " " + arg(&args, t)
		}
	}
	return a.list(w, r, "SELECT * FROM pulso_iot.mqtt_messages", where, "received_at DESC,id DESC", args)
}
func (a *App) ExpireCommands() error {
	_, e := a.DB.Exec(a.ctx, `UPDATE pulso_iot.device_commands SET status='timeout',error_message='Sem resposta no prazo configurado' WHERE status IN ('pending','published') AND issued_at < now()-($1*interval '1 second')`, a.C.CommandTimeout.Seconds())
	return e
}

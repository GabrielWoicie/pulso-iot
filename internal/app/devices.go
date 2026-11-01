package app

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	simconfig "github.com/woicie/iot-simulator/pkg/config"
	"log/slog"
	"net/http"
	"strings"
)

const deviceSelect = `SELECT d.id,d.name,d.device_identifier,d.device_type,d.device_mode,d.client_id,d.mqtt_username,d.status,d.description,d.default_qos,d.publish_interval_ms,d.last_seen_at,d.created_at,d.updated_at,d.deleted_at,c.provisioning_status,c.credential_version,c.provisioned_at,c.rotated_at,c.revoked_at,c.last_error,(c.password_ciphertext IS NOT NULL) AS has_mqtt_password FROM pulso_iot.devices d LEFT JOIN pulso_iot.mqtt_credentials c ON c.device_id=d.id`

func (a *App) devices(w http.ResponseWriter, r *http.Request) error {
	if values, ok := r.URL.Query()["device_mode"]; ok {
		if len(values) != 1 || !validDeviceMode(values[0]) {
			return problem(400, "INVALID_DEVICE_MODE", "Use physical ou simulated")
		}
	}
	where, args := filters(r, map[string]string{"status": "d.status", "type": "d.device_type", "device_mode": "d.device_mode"})
	where += " AND d.deleted_at IS NULL"
	if s := r.URL.Query().Get("search"); s != "" {
		p := arg(&args, "%"+s+"%")
		where += " AND (d.name ILIKE " + p + " OR d.device_identifier ILIKE " + p + ")"
	}
	order := "d.created_at DESC,d.id"
	switch r.URL.Query().Get("sort") {
	case "", "created_at":
	case "name":
		order = "d.name,d.id"
	case "device_identifier":
		order = "d.device_identifier,d.id"
	default:
		return problem(400, "INVALID_SORT", "sort inválido")
	}
	return a.list(w, r, deviceSelect, where, order, args)
}
func (a *App) device(w http.ResponseWriter, r *http.Request) error {
	id, e := requireID(r)
	if e != nil {
		return e
	}
	v, e := oneJSON(r.Context(), a.DB, deviceSelect+" WHERE d.id=$1 AND d.deleted_at IS NULL", id)
	if e != nil {
		return e
	}
	rows, e := queryJSON(r.Context(), a.DB, "SELECT topic_kind,topic,permission_type,qos_default FROM pulso_iot.device_topics WHERE device_id=$1 ORDER BY topic_kind", id)
	if e != nil {
		return e
	}
	v["topics"] = rows
	online, e := a.Broker.Online(r.Context(), v["client_id"].(string))
	v["online"] = nil
	v["online_source"] = "emqx"
	if e == nil {
		v["online"] = online
	}
	data(w, 200, v)
	return nil
}
func (a *App) topics(w http.ResponseWriter, r *http.Request) error {
	id, e := requireID(r)
	if e != nil {
		return e
	}
	var exists bool
	if e = a.DB.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM pulso_iot.devices WHERE id=$1 AND deleted_at IS NULL)", id).Scan(&exists); e != nil {
		return e
	}
	if !exists {
		return pgx.ErrNoRows
	}
	v, e := queryJSON(r.Context(), a.DB, "SELECT topic_kind,topic,permission_type,qos_default FROM pulso_iot.device_topics WHERE device_id=$1 ORDER BY topic_kind", id)
	if e == nil {
		data(w, 200, v)
	}
	return e
}

func validDeviceMode(s string) bool { return s == "physical" || s == "simulated" }

// RawMessage distinguishes omission from null and preserves type validation as 422.
func parseDeviceMode(raw json.RawMessage) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || !validDeviceMode(value) {
		return nil, problem(422, "INVALID_DEVICE_MODE", "Use physical ou simulated")
	}
	return &value, nil
}

type deviceBody struct {
	Mode        json.RawMessage `json:"device_mode"`
	Name        string          `json:"name"`
	Identifier  string          `json:"device_identifier"`
	Type        string          `json:"device_type"`
	Description string          `json:"description"`
	QoS         *int            `json:"default_qos"`
	Interval    int             `json:"publish_interval_ms"`
	Password    string          `json:"mqtt_password"`
}

func topicRows(ctx context.Context, db DB, id, identifier string, qos int) error {
	for _, k := range []string{"telemetry", "commands", "command-responses"} {
		p := "publish"
		if k == "commands" {
			p = "subscribe"
		}
		if _, e := db.Exec(ctx, `INSERT INTO pulso_iot.device_topics(device_id,topic_kind,topic,permission_type,qos_default) VALUES($1,$2,$3,$4,$5) ON CONFLICT(device_id,topic_kind) DO UPDATE SET qos_default=excluded.qos_default`, id, k, Prefix+identifier+"/"+k, p, qos); e != nil {
			return e
		}
	}
	return nil
}
func (a *App) createDevice(w http.ResponseWriter, r *http.Request) error {
	var b deviceBody
	if e := decode(w, r, &b); e != nil {
		return e
	}
	mode, err := parseDeviceMode(b.Mode)
	if err != nil {
		return err
	}
	if mode == nil {
		value := "physical"
		mode = &value
	}
	b.Name = strings.TrimSpace(b.Name)
	if b.Type == "" {
		b.Type = "sensor"
	}
	if b.Interval == 0 {
		b.Interval = 5000
	}
	q := 1
	if b.QoS != nil {
		q = *b.QoS
	}
	if b.Name == "" || len(b.Name) > 150 || !simconfig.ValidID(b.Identifier) || len(b.Identifier) > 100 || len(b.Type) > 40 || b.Interval < 100 || b.Interval > 3600000 || q < 0 || q > 2 || len(b.Description) > 4000 {
		return problem(422, "INVALID_DEVICE", "Parâmetros de dispositivo inválidos")
	}
	if b.Password == "" {
		b.Password = RandomSecret()
	}
	if !validMQTTPassword(b.Password) {
		return problem(422, "WEAK_MQTT_PASSWORD", "Senha MQTT exige 12–256 bytes")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopping {
		return problem(503, "SHUTTING_DOWN", "Serviço encerrando")
	}
	id := uuid.NewString()
	cipher, e := Seal(a.C.Key, id, b.Password)
	if e != nil {
		return e
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	_, e = tx.Exec(r.Context(), `INSERT INTO pulso_iot.devices(id,name,device_identifier,client_id,mqtt_username,device_type,description,default_qos,publish_interval_ms,status,device_mode) VALUES($1,$2,$3,$3,$3,$4,$5,$6,$7,'active',$8)`, id, b.Name, b.Identifier, b.Type, b.Description, q, b.Interval, *mode)
	if e != nil {
		return e
	}
	_, e = tx.Exec(r.Context(), `INSERT INTO pulso_iot.mqtt_credentials(device_id,password_ciphertext,password_saved_at) VALUES($1,$2,now())`, id, cipher)
	if e != nil {
		return e
	}
	if e = topicRows(r.Context(), tx, id, b.Identifier, q); e != nil {
		return e
	}
	if e = audit(r.Context(), tx, actor(r), "device.created", "devices", id); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	e = a.syncDevice(r.Context(), id, false)
	if e != nil {
		_ = dbError(e)
		write(w, 503, map[string]any{"error": map[string]string{"code": "PROVISIONING_PENDING", "message": "Cadastro salvo; provisionamento requer reconciliação", "device_id": id}, "data": map[string]any{"id": id, "mqtt_password_once": b.Password}})
		return nil
	}
	v, e := oneJSON(r.Context(), a.DB, deviceSelect+" WHERE d.id=$1", id)
	if e == nil {
		v["mqtt_password_once"] = b.Password
		data(w, 201, v)
	}
	return e
}

// syncDevice commits intent before broker IO and records sanitized failure. Caller owns a.mu.
func (a *App) syncDevice(ctx context.Context, id string, allowReplace bool) error {
	var client, status string
	var deleted bool
	var cipher []byte
	var provisioned bool
	e := a.DB.QueryRow(ctx, `SELECT d.client_id,d.status,d.deleted_at IS NOT NULL,c.password_ciphertext,c.provisioned_at IS NOT NULL FROM pulso_iot.devices d JOIN pulso_iot.mqtt_credentials c ON c.device_id=d.id WHERE d.id=$1`, id).Scan(&client, &status, &deleted, &cipher, &provisioned)
	if e != nil {
		return e
	}
	if _, e = a.DB.Exec(ctx, "UPDATE pulso_iot.mqtt_credentials SET provisioning_status='pending',last_error=NULL WHERE device_id=$1", id); e != nil {
		return e
	}
	if deleted || status == "inactive" {
		e = a.Broker.Revoke(ctx, client)
	} else {
		var password string
		password, e = Open(a.C.Key, id, cipher)
		if e == nil {
			e = a.Broker.Provision(ctx, client, password, allowReplace || provisioned)
		}
	}
	// Even a disconnected HTTP caller must leave a visible, recoverable state.
	save, cancel := context.WithTimeout(context.Background(), 8*1e9)
	defer cancel()
	if e != nil {
		slog.Warn("broker synchronization failed", "device_id", id, "reason", e.Error())
		_, saveErr := a.DB.Exec(save, "UPDATE pulso_iot.mqtt_credentials SET provisioning_status='error',last_error='BROKER_SYNC_FAILED: revisar contrato, credencial e reconciliar' WHERE device_id=$1", id)
		if saveErr != nil {
			return saveErr
		}
		return problem(503, "BROKER_SYNC_FAILED", "Estado salvo; sincronização MQTT pendente")
	}
	state := "active"
	if deleted || status == "inactive" {
		state = "revoked"
	}
	_, e = a.DB.Exec(save, `UPDATE pulso_iot.mqtt_credentials SET provisioning_status=$2::text,last_error=NULL,provisioned_at=CASE WHEN $2='active' THEN now() ELSE provisioned_at END,revoked_at=CASE WHEN $2='revoked' THEN now() ELSE NULL END WHERE device_id=$1`, id, state)
	if e != nil {
		return e
	}
	return audit(save, a.DB, "", "device."+state, "devices", id)
}
func (a *App) mutationReady(id string) error {
	if a.stopping {
		return problem(503, "SHUTTING_DOWN", "Serviço encerrando")
	}
	if a.busy[id] {
		return problem(409, "DEVICE_BUSY", "Dispositivo reservado por teste funcional")
	}
	return nil
}
func (a *App) updateDevice(w http.ResponseWriter, r *http.Request) error {
	id, e := requireID(r)
	if e != nil {
		return e
	}
	var b struct {
		Mode        json.RawMessage `json:"device_mode"`
		Name        *string         `json:"name"`
		Type        *string         `json:"device_type"`
		Description *string         `json:"description"`
		QoS         *int            `json:"default_qos"`
		Interval    *int            `json:"publish_interval_ms"`
		Status      *string         `json:"status"`
	}
	if e = decode(w, r, &b); e != nil {
		return e
	}
	mode, err := parseDeviceMode(b.Mode)
	if err != nil {
		return err
	}
	if b.Name != nil && (strings.TrimSpace(*b.Name) == "" || len(*b.Name) > 150) || b.Type != nil && (len(*b.Type) < 1 || len(*b.Type) > 40) || b.Description != nil && len(*b.Description) > 4000 || b.QoS != nil && (*b.QoS < 0 || *b.QoS > 2) || b.Interval != nil && (*b.Interval < 100 || *b.Interval > 3600000) || b.Status != nil && *b.Status != "active" && *b.Status != "inactive" {
		return problem(422, "INVALID_DEVICE", "Campos inválidos")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if e = a.mutationReady(id); e != nil {
		return e
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	var client, oldMode string
	var qos int
	if e = tx.QueryRow(r.Context(), "SELECT client_id,device_mode FROM pulso_iot.devices WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", id).Scan(&client, &oldMode); e != nil {
		return e
	}
	if mode != nil && *mode != oldMode {
		online, err := a.Broker.Online(r.Context(), client)
		if err != nil {
			return problem(503, "BROKER_UNAVAILABLE", "Nao foi possivel verificar sessao MQTT")
		}
		if online {
			return problem(409, "CLIENT_CONNECTED", "Desconecte o cliente antes de alterar a classificacao")
		}
		if e = audit(r.Context(), tx, actor(r), "device.mode_changed."+oldMode+".to."+*mode, "devices", id); e != nil {
			return e
		}
	}
	e = tx.QueryRow(r.Context(), `UPDATE pulso_iot.devices SET name=COALESCE($2,name),device_type=COALESCE($3,device_type),description=COALESCE($4,description),default_qos=COALESCE($5,default_qos),publish_interval_ms=COALESCE($6,publish_interval_ms),status=COALESCE($7,status),device_mode=COALESCE($8,device_mode) WHERE id=$1 AND deleted_at IS NULL RETURNING client_id,default_qos`, id, b.Name, b.Type, b.Description, b.QoS, b.Interval, b.Status, mode).Scan(&client, &qos)
	if e != nil {
		return e
	}
	if e = topicRows(r.Context(), tx, id, client, qos); e != nil {
		return e
	}
	if e = audit(r.Context(), tx, actor(r), "device.updated", "devices", id); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	if b.Status != nil {
		if e = a.syncDevice(r.Context(), id, false); e != nil {
			return e
		}
	}
	return a.device(w, r)
}
func (a *App) deleteDevice(w http.ResponseWriter, r *http.Request) error {
	id, e := requireID(r)
	if e != nil {
		return e
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if e = a.mutationReady(id); e != nil {
		return e
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	tag, e := tx.Exec(r.Context(), "UPDATE pulso_iot.devices SET deleted_at=COALESCE(deleted_at,now()),status='inactive' WHERE id=$1", id)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if e = audit(r.Context(), tx, actor(r), "device.deleted", "devices", id); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	if e = a.syncDevice(r.Context(), id, false); e != nil {
		return e
	}
	data(w, 200, map[string]any{"id": id, "deleted": true, "provisioning_status": "revoked"})
	return nil
}
func (a *App) reconcileDevice(w http.ResponseWriter, r *http.Request) error {
	id, e := requireID(r)
	if e != nil {
		return e
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if e = a.mutationReady(id); e != nil {
		return e
	}
	if e = audit(r.Context(), a.DB, actor(r), "device.reconcile_requested", "devices", id); e != nil {
		return e
	}
	if e = a.syncDevice(r.Context(), id, false); e != nil {
		return e
	}
	data(w, 200, map[string]string{"id": id, "result": "reconciled"})
	return nil
}
func (a *App) rotateDevice(w http.ResponseWriter, r *http.Request) error {
	return a.saveCredential(w, r, false)
}
func (a *App) importCredential(w http.ResponseWriter, r *http.Request) error {
	return a.saveCredential(w, r, true)
}
func (a *App) saveCredential(w http.ResponseWriter, r *http.Request, importing bool) error {
	id, e := requireID(r)
	if e != nil {
		return e
	}
	var b struct {
		Password string `json:"mqtt_password"`
	}
	if e = decode(w, r, &b); e != nil {
		return e
	}
	if importing && b.Password == "" {
		return problem(422, "PASSWORD_REQUIRED", "Informe a senha MQTT atual")
	}
	if b.Password == "" {
		b.Password = RandomSecret()
	}
	if !validMQTTPassword(b.Password) {
		return problem(422, "WEAK_MQTT_PASSWORD", "Senha MQTT exige 12–256 bytes")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if e = a.mutationReady(id); e != nil {
		return e
	}
	var client, status string
	if e = a.DB.QueryRow(r.Context(), "SELECT client_id,status FROM pulso_iot.devices WHERE id=$1 AND deleted_at IS NULL", id).Scan(&client, &status); e != nil {
		return e
	}
	if status != "active" {
		return problem(409, "DEVICE_INACTIVE", "Ative o dispositivo antes de alterar credencial")
	}
	if importing {
		online, err := a.Broker.Online(r.Context(), client)
		if err != nil {
			return problem(503, "BROKER_UNAVAILABLE", "Não foi possível verificar sessão MQTT")
		}
		if online {
			return problem(409, "CLIENT_CONNECTED", "Desconecte o cliente antes da importação")
		}
		if e = VerifyCredential(r.Context(), a.C, client, b.Password); e != nil {
			return problem(422, "CREDENTIAL_NOT_VERIFIED", "Conexão TLS com a credencial atual falhou")
		}
	}
	cipher, e := Seal(a.C.Key, id, b.Password)
	if e != nil {
		return e
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	_, e = tx.Exec(r.Context(), `INSERT INTO pulso_iot.mqtt_credentials(device_id,password_ciphertext,password_saved_at) VALUES($1,$2,now()) ON CONFLICT(device_id) DO UPDATE SET password_ciphertext=$2,password_saved_at=now(),credential_version=pulso_iot.mqtt_credentials.credential_version+1,rotated_at=now(),provisioning_status='pending',last_error=NULL`, id, cipher)
	if e != nil {
		return e
	}
	action := "device.credential_rotated"
	if importing {
		action = "device.credential_imported"
	}
	if e = audit(r.Context(), tx, actor(r), action, "devices", id); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	if e = a.syncDevice(r.Context(), id, true); e != nil {
		return e
	}
	out := map[string]any{"id": id, "provisioning_status": "active"}
	if !importing {
		out["mqtt_password_once"] = b.Password
	}
	data(w, 200, out)
	return nil
}

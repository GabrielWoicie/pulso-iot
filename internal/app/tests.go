package app

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	simconfig "github.com/woicie/iot-simulator/pkg/config"
	"github.com/woicie/iot-simulator/pkg/runner"
	"log/slog"
	"net/http"
	"time"
)

func validDeviceID(s string) bool { return len(s) <= 100 && simconfig.ValidID(s) }

type testBody struct {
	DeviceIDs []string `json:"device_ids"`
	Messages  int      `json:"messages"`
	Interval  int      `json:"interval_ms"`
	QoS       int      `json:"qos"`
}

func (c Config) validateTest(b testBody) error {
	if len(b.DeviceIDs) < 1 || len(b.DeviceIDs) > c.MaxDevices || b.Messages < 1 || b.Messages > c.MaxMessages || b.Interval < 100 || b.Interval > 60000 || int64(b.Interval)*int64(b.Messages) > int64((15*time.Minute).Milliseconds()) || b.QoS < 0 || b.QoS > 2 {
		return problem(422, "INVALID_TEST", "Limites: 1–10 dispositivos, 1–1000 mensagens, intervalo 100–60000 ms e duração de publicação até 15 minutos")
	}
	seen := map[string]bool{}
	for _, id := range b.DeviceIDs {
		if !validDeviceID(id) || seen[id] {
			return problem(422, "INVALID_TEST_DEVICE", "Identificador invalido ou selecao duplicada")
		}
		seen[id] = true
	}
	return nil
}
func (a *App) createTest(w http.ResponseWriter, r *http.Request) error {
	var b testBody
	if e := decode(w, r, &b); e != nil {
		return e
	}
	if e := a.C.validateTest(b); e != nil {
		return e
	}
	if a.MQTT == nil || !a.MQTT.Ready() {
		return problem(503, "MQTT_UNAVAILABLE", "Consumidor MQTT precisa estar conectado")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopping {
		return problem(503, "SHUTTING_DOWN", "Serviço encerrando")
	}
	if len(a.active) >= 1 {
		return problem(409, "TEST_CAPACITY", "Já existe teste em andamento")
	}
	// Validate persisted classification for the entire selection before any runner work.
	for _, identifier := range b.DeviceIDs {
		var mode string
		err := a.DB.QueryRow(r.Context(), "SELECT device_mode FROM pulso_iot.devices WHERE device_identifier=$1 AND deleted_at IS NULL", identifier).Scan(&mode)
		if err == pgx.ErrNoRows {
			return problem(422, "DEVICE_NOT_READY", "Dispositivo indisponivel")
		}
		if err != nil {
			return err
		}
		if mode != "simulated" {
			return problem(422, "DEVICE_NOT_SIMULATED", "Selecione somente dispositivos simulated")
		}
	}
	cfg := simconfig.Defaults()
	cfg.Broker = a.C.MQTTBroker
	cfg.QoS = b.QoS
	cfg.Interval = simconfig.Duration(time.Duration(b.Interval) * time.Millisecond)
	ids := map[string]string{}
	for _, identifier := range b.DeviceIDs {
		var id, username, mode string
		var cipher []byte
		e := a.DB.QueryRow(r.Context(), `SELECT d.id::text,d.mqtt_username,c.password_ciphertext,d.device_mode FROM pulso_iot.devices d JOIN pulso_iot.mqtt_credentials c ON c.device_id=d.id WHERE d.device_identifier=$1 AND d.status='active' AND d.deleted_at IS NULL AND c.provisioning_status='active' AND c.password_key_version=1`, identifier).Scan(&id, &username, &cipher, &mode)
		if e != nil {
			if e == pgx.ErrNoRows {
				return problem(422, "DEVICE_NOT_READY", "Dispositivo não está ativo/provisionado")
			}
			return e
		}
		if mode != "simulated" {
			return problem(422, "DEVICE_NOT_SIMULATED", "Selecione somente dispositivos simulated")
		}
		password, e := Open(a.C.Key, id, cipher)
		if e != nil {
			return problem(422, "CREDENTIAL_UNAVAILABLE", "Inclua ou gire a senha MQTT antes do teste")
		}
		online, e := a.Broker.Online(r.Context(), identifier)
		if e != nil {
			return problem(503, "BROKER_UNAVAILABLE", "Não foi possível verificar sessões MQTT")
		}
		if online || a.busy[id] {
			return problem(409, "CLIENT_CONNECTED", "Dispositivo conectado ou reservado; desconecte o cliente existente")
		}
		q := b.QoS
		cfg.Devices = append(cfg.Devices, simconfig.Device{ID: identifier, ClientID: identifier, Username: username, Password: password, QoS: &q, Telemetry: simconfig.Sensors{Temperature: simconfig.Range{Min: 20, Max: 30}, Humidity: simconfig.Range{Min: 40, Max: 70}}})
		ids[identifier] = id
	}
	run, e := runner.New(cfg, a.Factory)
	if e != nil {
		return problem(422, "INVALID_RUNNER_CONFIG", "Configuração do runner inválida")
	}
	id := uuid.NewString()
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	_, e = tx.Exec(r.Context(), `INSERT INTO pulso_iot.test_runs(id,selected_device_ids,messages_per_device,interval_ms,qos,created_by) VALUES($1,$2,$3,$4,$5,$6)`, id, b.DeviceIDs, b.Messages, b.Interval, b.QoS, actor(r))
	if e != nil {
		return e
	}
	for _, did := range ids {
		if _, e = tx.Exec(r.Context(), "INSERT INTO pulso_iot.test_results(test_run_id,device_id) VALUES($1,$2)", id, did); e != nil {
			return e
		}
	}
	if e = audit(r.Context(), tx, actor(r), "test.created", "test_runs", id); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(a.ctx, time.Duration(b.Messages*b.Interval)*time.Millisecond+45*time.Second)
	a.active[id] = cancel
	for _, did := range ids {
		a.busy[did] = true
	}
	a.wg.Add(1)
	go a.runTest(ctx, id, ids, run, b)
	data(w, 202, map[string]string{"id": id, "status": "queued"})
	return nil
}
func (a *App) runTest(ctx context.Context, id string, ids map[string]string, run *runner.Runner, b testBody) {
	defer a.wg.Done()
	defer func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if cancel, ok := a.active[id]; ok {
			cancel()
			delete(a.active, id)
		}
		for _, did := range ids {
			delete(a.busy, did)
		}
	}()
	save, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	_, e := a.DB.Exec(save, "UPDATE pulso_iot.test_runs SET status='running',started_at=now() WHERE id=$1", id)
	cancel()
	if e != nil {
		slog.Error("test start persistence failed", "test_run_id", id)
		return
	}
	result, runErr := run.Run(ctx, runner.RunRequest{Messages: b.Messages, Interval: time.Duration(b.Interval) * time.Millisecond, QoS: &b.QoS}, nil)
	status := result.Status
	if status == "" {
		status = "failed"
	}
	if ctx.Err() == context.DeadlineExceeded {
		status = "failed"
	}
	save, cancel = context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	tx, e := a.DB.Begin(save)
	if e != nil {
		slog.Error("test result persistence failed", "test_run_id", id)
		return
	}
	defer tx.Rollback(save)
	summary, _ := json.Marshal(map[string]any{"runner_run_id": result.RunID, "requested_devices": result.RequestedDevices, "messages_per_device": b.Messages, "received_by_backend": nil, "metric_note": "PUBACK não mede recebimento pelo backend; latências não instrumentadas"})
	var reason any
	if runErr != nil {
		reason = "RUN_FAILED_OR_CANCELLED: consulte contadores; credenciais omitidas"
	}
	_, e = tx.Exec(save, `UPDATE pulso_iot.test_runs SET status=$2,summary=$3,error_message=$4,finished_at=now() WHERE id=$1`, id, status, json.RawMessage(summary), reason)
	if e != nil {
		slog.Error("test final status failed", "test_run_id", id)
		return
	}
	for _, d := range result.Devices {
		ds := "completed"
		if status == "cancelled" {
			ds = "cancelled"
		} else if d.Fatal || d.Failed > 0 || status == "failed" {
			ds = "failed"
		}
		_, e = tx.Exec(save, `UPDATE pulso_iot.test_results SET status=$3,attempted=$4,published=$5,acknowledged_by_broker=$6,failed=$7,commands_received=$8,responses_published=$9,started_at=$10,finished_at=$11,details=jsonb_build_object('runner_run_id',$12::text,'qos',$13::int) WHERE test_run_id=$1 AND device_id=$2`, id, ids[d.DeviceID], ds, d.Attempted, d.Published, d.BrokerAcknowledged, d.Failed, d.CommandsReceived, d.ResponsesPublished, result.StartedAt, result.FinishedAt, result.RunID, d.QoS)
		if e != nil {
			slog.Error("device result persistence failed", "test_run_id", id)
			return
		}
	}
	if _, e = tx.Exec(save, "UPDATE pulso_iot.test_results SET status=$2,finished_at=now() WHERE test_run_id=$1 AND status='pending'", id, status); e != nil {
		return
	}
	if e = audit(save, tx, "", "test."+status, "test_runs", id); e != nil {
		return
	}
	if e = tx.Commit(save); e != nil {
		slog.Error("test result commit failed", "test_run_id", id)
	}
}
func (a *App) tests(w http.ResponseWriter, r *http.Request) error {
	q, args := filters(r, map[string]string{"status": "status"})
	return a.list(w, r, "SELECT * FROM pulso_iot.test_runs", q, "created_at DESC,id", args)
}
func (a *App) test(w http.ResponseWriter, r *http.Request) error {
	id, e := requireID(r)
	if e != nil {
		return e
	}
	v, e := oneJSON(r.Context(), a.DB, "SELECT * FROM pulso_iot.test_runs WHERE id=$1", id)
	if e != nil {
		return e
	}
	rows, e := queryJSON(r.Context(), a.DB, "SELECT r.*,d.device_identifier FROM pulso_iot.test_results r JOIN pulso_iot.devices d ON d.id=r.device_id WHERE test_run_id=$1 ORDER BY d.device_identifier", id)
	if e != nil {
		return e
	}
	v["results"] = rows
	data(w, 200, v)
	return nil
}
func (a *App) cancelTest(w http.ResponseWriter, r *http.Request) error {
	id, e := requireID(r)
	if e != nil {
		return e
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	cancel, ok := a.active[id]
	if !ok {
		var status string
		if e = a.DB.QueryRow(r.Context(), "SELECT status FROM pulso_iot.test_runs WHERE id=$1", id).Scan(&status); e != nil {
			return e
		}
		return problem(409, "TEST_NOT_RUNNING", "Teste não está em execução")
	}
	if e = audit(r.Context(), a.DB, actor(r), "test.cancel_requested", "test_runs", id); e != nil {
		return e
	}
	cancel()
	data(w, 202, map[string]any{"id": id, "cancellation_requested": true})
	return nil
}

// Recover must run only while holding the process-wide PostgreSQL advisory lock.
func (a *App) Recover(ctx context.Context) error {
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `UPDATE pulso_iot.test_results SET status='failed',finished_at=now(),details=details||'{"interrupted":true}'::jsonb WHERE test_run_id IN (SELECT id FROM pulso_iot.test_runs WHERE status IN ('queued','running'))`); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE pulso_iot.test_runs SET status='failed',finished_at=now(),error_message='PROCESS_INTERRUPTED: contadores podem estar incompletos' WHERE status IN ('queued','running')`); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

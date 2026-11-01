package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/woicie/iot-simulator/pkg/runner"
	"github.com/woicie/pulso-iot/internal/emqx"
	"io"
	"log/slog"
	"mime"
	"net/http"

	"sync"
	"time"
)

type App struct {
	C        Config
	DB       *pgxpool.Pool
	Broker   *emqx.Client
	MQTT     Publisher
	Factory  runner.Factory
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.Mutex
	active   map[string]context.CancelFunc
	busy     map[string]bool
	stopping bool
	loginMu  sync.Mutex
	attempts map[string]attempt
	Metrics  Metrics
}
type attempt struct {
	Count int
	Until time.Time
}

func New(ctx context.Context, c Config, db *pgxpool.Pool) *App {
	ctx, cancel := context.WithCancel(ctx)
	return &App{C: c, DB: db, Broker: emqx.New(c.EMQXURL, c.EMQXKey, c.EMQXSecret), ctx: ctx, cancel: cancel, active: map[string]context.CancelFunc{}, busy: map[string]bool{}, attempts: map[string]attempt{}}
}
func (a *App) Close() {
	a.mu.Lock()
	a.stopping = true
	a.cancel()
	for _, c := range a.active {
		c()
	}
	a.mu.Unlock()
	a.wg.Wait()
}

type Problem struct {
	Status        int
	Code, Message string
}

func (e *Problem) Error() string       { return e.Message }
func problem(s int, c, m string) error { return &Problem{s, c, m} }

type endpoint func(http.ResponseWriter, *http.Request) error

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func data(w http.ResponseWriter, status int, v any) { write(w, status, map[string]any{"data": v}) }
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	contentType, _, parseErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if parseErr != nil || contentType != "application/json" {
		return problem(400, "JSON_REQUIRED", "Use application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	raw, e := io.ReadAll(r.Body)
	if e != nil || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return problem(400, "INVALID_JSON", "Envie um objeto JSON")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return problem(400, "INVALID_JSON", "Corpo JSON inválido ou campo desconhecido")
	}
	var x any
	if d.Decode(&x) != io.EOF {
		return problem(400, "INVALID_JSON", "Envie apenas um objeto JSON")
	}
	return nil
}
func (a *App) wrap(role string, h endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		var e error
		if role != "" {
			r, e = a.authenticate(r, role)
		}
		if e == nil {
			e = h(w, r)
		}
		if e != nil {
			var p *Problem
			if !errors.As(e, &p) {
				e = dbError(e)
				p = e.(*Problem)
			}
			if p.Status >= 500 {
				slog.Error("http operation failed", "request_id", w.Header().Get("X-Request-ID"), "route", r.Pattern, "code", p.Code)
			}
			write(w, p.Status, map[string]any{"error": map[string]string{"code": p.Code, "message": p.Message}})
		}
	}
}
func (a *App) Handler() http.Handler {
	m := http.NewServeMux()
	routes := []struct {
		pattern, role string
		h             endpoint
	}{
		{"POST /api/v1/auth/login", "", a.login}, {"GET /api/v1/auth/me", "read", a.me},
		{"GET /api/v1/users", "admin", a.users}, {"GET /api/v1/users/{id}", "admin", a.user}, {"POST /api/v1/users", "admin", a.createUser}, {"PATCH /api/v1/users/{id}", "admin", a.updateUser}, {"DELETE /api/v1/users/{id}", "admin", a.updateUser}, {"PATCH /api/v1/users/{id}/password", "admin", a.resetPassword},
		{"GET /api/v1/devices", "read", a.devices}, {"GET /api/v1/devices/{id}", "read", a.device}, {"POST /api/v1/devices", "write", a.createDevice}, {"PATCH /api/v1/devices/{id}", "write", a.updateDevice}, {"DELETE /api/v1/devices/{id}", "admin", a.deleteDevice}, {"POST /api/v1/devices/{id}/credentials/rotate", "write", a.rotateDevice}, {"POST /api/v1/devices/{id}/credentials/import", "admin", a.importCredential}, {"POST /api/v1/devices/{id}/reconcile", "write", a.reconcileDevice}, {"POST /api/v1/devices/{id}/provision", "write", a.reconcileDevice}, {"GET /api/v1/devices/{id}/topics", "read", a.topics},
		{"GET /api/v1/messages", "read", a.messages}, {"GET /api/v1/devices/{id}/telemetry", "read", a.messages}, {"GET /api/v1/devices/{id}/messages", "read", a.messages},
		{"POST /api/v1/devices/{id}/commands", "write", a.sendCommand}, {"GET /api/v1/devices/{id}/commands", "read", a.commands}, {"GET /api/v1/commands/{id}", "read", a.command},
		{"POST /api/v1/tests", "write", a.createTest}, {"GET /api/v1/tests", "read", a.tests}, {"GET /api/v1/tests/{id}", "read", a.test}, {"POST /api/v1/tests/{id}/cancel", "write", a.cancelTest},
		{"GET /api/v1/dashboard/summary", "read", a.summary}, {"GET /api/v1/dashboard/recent-messages", "read", a.messages}, {"GET /api/v1/dashboard/message-volume", "read", a.volume}, {"GET /api/v1/broker/status", "read", a.brokerStatus}, {"GET /api/v1/audit-logs", "admin", a.auditLogs}, {"GET /api/v1/health", "", a.health},
	}
	for _, v := range routes {
		m.HandleFunc(v.pattern, a.wrap(v.role, v.h))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := uuid.NewString()
		w.Header().Set("X-Request-ID", rid)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if a.C.Origin != "" && r.Header.Get("Origin") == a.C.Origin {
			w.Header().Set("Access-Control-Allow-Origin", a.C.Origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PATCH,DELETE,OPTIONS")
			if r.Method == "OPTIONS" {
				w.WriteHeader(204)
				return
			}
		}
		defer func() {
			if recover() != nil {
				slog.Error("http panic", "request_id", rid)
				write(w, 500, map[string]any{"error": map[string]string{"code": "INTERNAL_ERROR", "message": "Falha interna"}})
			}
		}()
		m.ServeHTTP(w, r)
	})
}
func (a *App) list(w http.ResponseWriter, r *http.Request, selectSQL, where, order string, args []any) error {
	p, e := boundedInt(r.URL.Query().Get("page"), 1, 1, 1000000)
	if e != nil {
		return problem(400, "INVALID_PAGE", e.Error())
	}
	l, e := boundedInt(r.URL.Query().Get("limit"), 20, 1, 100)
	if e != nil {
		return problem(400, "INVALID_LIMIT", e.Error())
	}
	var n int
	e = a.DB.QueryRow(r.Context(), "SELECT count(*) FROM ("+selectSQL+" WHERE "+where+") c", args...).Scan(&n)
	if e != nil {
		return e
	}
	q := selectSQL + " WHERE " + where + " ORDER BY " + order + " LIMIT " + arg(&args, l) + " OFFSET " + arg(&args, (p-1)*l)
	rows, e := queryJSON(r.Context(), a.DB, q, args...)
	if e != nil {
		return e
	}
	write(w, 200, map[string]any{"data": rows, "pagination": map[string]int{"currentPage": p, "totalPages": (n + l - 1) / l, "totalCount": n, "limit": l}})
	return nil
}
func requireID(r *http.Request) (string, error) {
	s := r.PathValue("id")
	u, e := uuid.Parse(s)
	if e != nil {
		return "", problem(400, "INVALID_ID", "ID deve ser UUID")
	}
	return u.String(), nil
}
func filters(r *http.Request, allowed map[string]string) (string, []any) {
	w := "TRUE"
	args := []any{}
	for key, col := range allowed {
		if v := r.URL.Query().Get(key); v != "" {
			w += " AND " + col + " = " + arg(&args, v)
		}
	}
	return w, args
}

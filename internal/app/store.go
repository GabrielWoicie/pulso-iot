package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"log/slog"
	"strings"
	"time"
)

type DB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	c, e := pgxpool.ParseConfig(url)
	if e != nil {
		return nil, errors.New("DATABASE_URL inválida")
	}
	if !strings.Contains(url, "pool_max_conns=") {
		c.MaxConns = 12
	}
	if c.MaxConns < 1 || c.MaxConns > 32 {
		return nil, errors.New("pool_max_conns deve estar entre 1 e 32")
	}
	c.MinConns = 1
	c.ConnConfig.ConnectTimeout = 5 * time.Second
	c.ConnConfig.RuntimeParams["timezone"] = "UTC"
	c.ConnConfig.RuntimeParams["statement_timeout"] = "8000"
	c.ConnConfig.RuntimeParams["search_path"] = "pg_catalog,pulso_iot"
	p, e := pgxpool.NewWithConfig(ctx, c)
	if e != nil {
		return nil, errors.New("PostgreSQL indisponível")
	}
	if e = p.Ping(ctx); e != nil {
		p.Close()
		return nil, errors.New("PostgreSQL indisponível")
	}
	return p, nil
}
func queryJSON(ctx context.Context, db DB, q string, args ...any) ([]map[string]any, error) {
	rows, e := db.Query(ctx, "SELECT row_to_json(x) FROM ("+q+") x", args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var b []byte
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		var v map[string]any
		if e = json.Unmarshal(b, &v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func oneJSON(ctx context.Context, db DB, q string, args ...any) (map[string]any, error) {
	v, e := queryJSON(ctx, db, q, args...)
	if e != nil {
		return nil, e
	}
	if len(v) == 0 {
		return nil, pgx.ErrNoRows
	}
	return v[0], nil
}
func audit(ctx context.Context, db DB, actor, action, entity, id string) error {
	_, e := db.Exec(ctx, `INSERT INTO pulso_iot.audit_logs(actor_user_id,actor_type,action,entity_type,entity_id) VALUES(NULLIF($1,'')::uuid,CASE WHEN $1='' THEN 'system' ELSE 'user' END,$2,$3,NULLIF($4,'')::uuid)`, actor, action, entity, id)
	return e
}
func dbError(e error) error {
	var p *pgconn.PgError
	if errors.As(e, &p) {
		slog.Error("database operation failed", "sqlstate", p.Code)
		switch p.Code {
		case "23505":
			return problem(409, "CONFLICT", "Registro já existe")
		case "22P02", "23514", "22001", "23503":
			return problem(422, "VALIDATION_ERROR", "Dados inválidos")
		}
	}
	if errors.Is(e, pgx.ErrNoRows) {
		return problem(404, "NOT_FOUND", "Registro não encontrado")
	}
	return problem(503, "DATABASE_UNAVAILABLE", "Operação indisponível; tente novamente")
}
func arg(args *[]any, v any) string { *args = append(*args, v); return fmt.Sprintf("$%d", len(*args)) }

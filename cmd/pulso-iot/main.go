package main

import (
	"context"
	"fmt"
	"github.com/woicie/pulso-iot/internal/app"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if e := run(); e != nil {
		slog.Error("startup failed", "reason", e.Error())
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) > 1 && os.Args[1] == "keygen" {
		fmt.Println(app.RandomSecret())
		return nil
	}
	if e := app.LoadDotEnv(".env"); e != nil {
		return e
	}
	c, e := app.LoadConfig()
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	db, e := app.Connect(startup, c.DatabaseURL)
	if e != nil {
		return e
	}
	defer db.Close()
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "bootstrap-admin":
			tx, e := db.Begin(startup)
			if e != nil {
				return fmt.Errorf("bootstrap indisponível")
			}
			defer tx.Rollback(startup)
			if _, e = tx.Exec(startup, "SELECT pg_advisory_xact_lock(741001)"); e != nil {
				return fmt.Errorf("bootstrap lock falhou")
			}
			if e = app.Bootstrap(startup, tx, os.Getenv("ADMIN_NAME"), os.Getenv("ADMIN_EMAIL"), os.Getenv("ADMIN_PASSWORD")); e != nil {
				return fmt.Errorf("bootstrap recusado: verifique dados, admin existente e schema")
			}
			if e = tx.Commit(startup); e != nil {
				return fmt.Errorf("bootstrap commit falhou")
			}
			slog.Info("first administrator created")
			return nil
		case "check-broker":
			a := app.New(ctx, c, db)
			defer a.Close()
			return a.Broker.Preflight(startup)
		default:
			return fmt.Errorf("subcomando inválido: bootstrap-admin | check-broker | keygen")
		}
	}
	if c.MQTTPassword == "" || c.EMQXKey == "" || c.EMQXSecret == "" {
		return fmt.Errorf("configure credenciais MQTT e API EMQX")
	}
	if db.Config().MaxConns < 2 {
		return fmt.Errorf("servidor exige pool_max_conns >= 2")
	}
	// One worker owner per database; a second instance must not recover live runs.
	lock, e := db.Acquire(startup)
	if e != nil {
		return fmt.Errorf("worker lock indisponível")
	}
	defer lock.Release()
	var owned bool
	if e = lock.QueryRow(startup, "SELECT pg_try_advisory_lock(741002)").Scan(&owned); e != nil || !owned {
		return fmt.Errorf("outra instância pulso-iot já está ativa neste banco")
	}
	defer lock.Exec(context.Background(), "SELECT pg_advisory_unlock(741002)")
	a := app.New(ctx, c, db)
	defer a.Close()
	if e = a.Broker.Preflight(startup); e != nil {
		return e
	}
	if e = a.Recover(startup); e != nil {
		return fmt.Errorf("schema inválido ou recuperação de testes falhou")
	}
	mqtt, e := a.StartMQTT()
	if e != nil {
		return e
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mqtt.Close(ctx)
	}()
	srv := &http.Server{Addr: c.Listen, Handler: a.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	errs := make(chan error, 1)
	go func() { slog.Info("http listening", "address", c.Listen); errs <- srv.ListenAndServe() }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdown)
			return nil
		case e := <-errs:
			if e != http.ErrServerClosed {
				return fmt.Errorf("servidor HTTP falhou")
			}
			return nil
		case <-ticker.C:
			if e = a.ExpireCommands(); e != nil && ctx.Err() == nil {
				slog.Error("command expiry persistence failed")
			}
			var one int
			check, cancel := context.WithTimeout(ctx, 3*time.Second)
			e = lock.QueryRow(check, "SELECT 1").Scan(&one)
			cancel()
			if e != nil {
				_ = srv.Close()
				return fmt.Errorf("worker lock connection lost; encerrando para preservar exclusividade")
			}
		}
	}
}

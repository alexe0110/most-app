// Main package
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/riverqueue/river"

	"github.com/alexe0110/most-app/internal/platform/config"
	"github.com/alexe0110/most-app/internal/platform/db"
	"github.com/alexe0110/most-app/internal/platform/jobs"
	"github.com/alexe0110/most-app/internal/platform/logging"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer func() {
		fmt.Println("стопаю сервис")
		fmt.Println("пу пу пу")
		time.Sleep(2 * time.Second)
		stop()
	}()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logging.New(cfg.LogLevel)

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := db.Migrate(ctx, cfg.DatabaseURL); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := jobs.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate river: %w", err)
	}
	log.Info("migrations applied")

	workers := river.NewWorkers()
	purge := jobs.RegisterPurgeUsers(workers, pool, log)
	queue, err := jobs.New(pool, log, workers, purge)
	if err != nil {
		return err
	}
	if err := queue.Start(ctx); err != nil {
		return fmt.Errorf("start queue: %w", err)
	}
	log.Info("job queue started")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: mux}

	go func() {
		log.Info("server started", "addr", cfg.HTTPAddr, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Stop intake first, then drain the workers, both within the same budget.
	srvErr := srv.Shutdown(shutdownCtx)
	queueErr := queue.Stop(shutdownCtx)
	return errors.Join(srvErr, queueErr)
}

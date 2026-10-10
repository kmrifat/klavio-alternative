// Command app is the single binary for the backend.
//
//	app -mode=migrate   apply database migrations and exit
//	app -mode=api       serve HTTP
//	app -mode=worker    run background jobs
//	app -mode=all       api + worker in one process (smallest deployments, local dev)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/kmrifat/klavio-alternative/api/internal/config"
	"github.com/kmrifat/klavio-alternative/api/internal/db"
	"github.com/kmrifat/klavio-alternative/api/internal/jobs"
	"github.com/kmrifat/klavio-alternative/api/internal/server"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	mode := flag.String("mode", "all", "migrate | api | worker | all")
	flag.Parse()

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(2)
	}
	log := cfg.Logger().With("mode", *mode)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, *mode, cfg, log); err != nil {
		log.Error("exiting", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, mode string, cfg config.Config, log *slog.Logger) error {
	switch mode {
	case "migrate", "api", "worker", "all":
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if mode == "migrate" {
		return db.Migrate(ctx, pool, log)
	}

	log.Info("starting", "version", version, "env", cfg.Env)
	g, ctx := errgroup.WithContext(ctx)
	if mode == "api" || mode == "all" {
		g.Go(func() error { return serveHTTP(ctx, cfg, log, pool) })
	}
	if mode == "worker" || mode == "all" {
		g.Go(func() error { return runWorker(ctx, log, pool) })
	}
	return g.Wait()
}

func serveHTTP(ctx context.Context, cfg config.Config, log *slog.Logger, pool *pgxpool.Pool) error {
	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: server.New(server.Options{
			Log:            log,
			DB:             pool,
			Version:        version,
			Env:            string(cfg.Env),
			AllowedOrigins: cfg.AllowedOrigins,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("http listening", "addr", cfg.HTTPAddr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	log.Info("http shutting down")
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func runWorker(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool) error {
	client, err := jobs.NewWorker(pool, log)
	if err != nil {
		return err
	}
	if err := client.Start(ctx); err != nil {
		return err
	}
	log.Info("worker started")

	<-ctx.Done()

	// Let running jobs finish, then give up after the timeout.
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	log.Info("worker stopping")
	return client.Stop(stopCtx)
}

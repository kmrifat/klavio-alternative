// Command app is the single binary for the backend.
//
//	app -mode=migrate   apply database migrations and exit
//	app -mode=api       serve HTTP
//	app -mode=worker    run background jobs
//	app -mode=all       api + worker in one process (smallest deployments, local dev)
//	app -mode=bootstrap -email you@company.com -name "Your Name"
//	                    print a one-time invite link for the first owner
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sync/errgroup"

	"github.com/kmrifat/klavio-alternative/api/internal/app"
	"github.com/kmrifat/klavio-alternative/api/internal/cache"
	"github.com/kmrifat/klavio-alternative/api/internal/config"
	"github.com/kmrifat/klavio-alternative/api/internal/db"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

type flags struct {
	mode, email, name string
}

func main() {
	var f flags
	flag.StringVar(&f.mode, "mode", "all", "migrate | api | worker | all | bootstrap")
	flag.StringVar(&f.email, "email", "", "bootstrap: email of the first owner")
	flag.StringVar(&f.name, "name", "", "bootstrap: name of the first owner")
	flag.Parse()

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(2)
	}
	log := cfg.Logger().With("mode", f.mode)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, f, cfg, log); err != nil {
		log.Error("exiting", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, f flags, cfg config.Config, log *slog.Logger) error {
	switch f.mode {
	case "migrate", "api", "worker", "all", "bootstrap":
	default:
		return fmt.Errorf("unknown mode %q", f.mode)
	}

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if f.mode == "migrate" {
		return db.Migrate(ctx, pool, log)
	}

	rdb, err := cache.Connect(ctx, cfg.RedisURL)
	if err != nil {
		return err
	}
	defer rdb.Close()

	a, err := app.FromConfig(ctx, cfg, log, pool, rdb, version)
	if err != nil {
		return err
	}

	if f.mode == "bootstrap" {
		link, err := a.Bootstrap(ctx, f.email, f.name)
		if err != nil {
			return err
		}
		fmt.Printf("\nOpen this link to create the first owner's passkey (valid 7 days, works once):\n\n  %s\n\n", link)
		return nil
	}

	log.Info("starting", "version", version, "env", cfg.Env, "ses", cfg.SESMode, "cloudflare_access", cfg.AccessTeamDomain != "",
		"trust_cloudflare_headers", cfg.TrustCloudflareHeaders, "trusted_proxies", cfg.TrustedProxies, "debug", cfg.Debug)
	if cfg.Debug && cfg.Env == config.EnvProduction {
		log.Warn("DEBUG is on in production: the admin API reference is served at /admin/v1/docs to signed-in users; turn it off when you're done")
	}
	g, ctx := errgroup.WithContext(ctx)
	if f.mode == "api" || f.mode == "all" {
		g.Go(func() error { return a.ServeHTTP(ctx) })
	}
	if f.mode == "worker" || f.mode == "all" {
		g.Go(func() error { return a.RunWorker(ctx) })
	}
	return g.Wait()
}

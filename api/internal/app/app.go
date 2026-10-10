// Package app wires the services together and builds the HTTP handler and
// job worker. cmd/app turns config into an App; tests build one with fakes.
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/brands"
	"github.com/kmrifat/klavio-alternative/api/internal/cache"
	"github.com/kmrifat/klavio-alternative/api/internal/config"
	"github.com/kmrifat/klavio-alternative/api/internal/dnscheck"
	"github.com/kmrifat/klavio-alternative/api/internal/jobs"
	"github.com/kmrifat/klavio-alternative/api/internal/server"
	"github.com/kmrifat/klavio-alternative/api/internal/ses"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/team"
)

type Deps struct {
	Config     config.Config
	Log        *slog.Logger
	Pool       *pgxpool.Pool
	Redis      server.Pinger
	Ceremonies auth.CeremonyStore
	Access     *auth.AccessVerifier // nil = Cloudflare Access off
	SES        ses.Provisioner
	DNS        dnscheck.Resolver
	Version    string
}

type App struct {
	deps   Deps
	Store  *store.Store
	Auth   *auth.Service
	Brands *brands.Service
}

func New(d Deps) (*App, error) {
	st := store.New(d.Pool)
	authSvc, err := auth.NewService(st, d.Ceremonies, d.Access, auth.Config{
		RPID:          d.Config.WebAuthnRPID,
		RPDisplayName: d.Config.WebAuthnRPName,
		RPOrigins:     d.Config.AllowedOrigins,
		SecureCookies: d.Config.SecureCookies(),
		AppURL:        d.Config.AppURL,
	}, d.Log)
	if err != nil {
		return nil, err
	}
	return &App{
		deps:   d,
		Store:  st,
		Auth:   authSvc,
		Brands: brands.NewService(st, d.SES, dnscheck.Checker{R: d.DNS}, d.Log),
	}, nil
}

// FromConfig builds the production dependencies: Redis-backed passkey
// ceremonies, Cloudflare Access when configured, real or fake SES, public DNS.
func FromConfig(ctx context.Context, cfg config.Config, log *slog.Logger, pool *pgxpool.Pool, rdb *cache.Client, version string) (*App, error) {
	d := Deps{
		Config: cfg, Log: log, Pool: pool, Redis: rdb, Version: version,
		Ceremonies: auth.RedisCeremonies{R: rdb.Client},
		SES:        &ses.Fake{},
		DNS:        dnscheck.PublicResolver(cfg.DNSResolver),
	}
	if cfg.AccessTeamDomain != "" {
		d.Access = auth.NewAccessVerifier(ctx, cfg.AccessTeamDomain, cfg.AccessAUD)
	}
	if cfg.SESMode == "aws" {
		aws, err := ses.NewAWS(ctx, cfg.SESRegion, cfg.SESEventBusARN)
		if err != nil {
			return nil, err
		}
		d.SES = aws
	}
	return New(d)
}

func (a *App) Handler() http.Handler {
	teamH := &team.Handlers{Store: a.Store, Auth: a.Auth, Log: a.deps.Log}
	return server.New(server.Options{
		Log:             a.deps.Log,
		DB:              a.deps.Pool,
		Redis:           a.deps.Redis,
		Version:         a.deps.Version,
		Env:             string(a.deps.Config.Env),
		AllowedOrigins:  a.deps.Config.AllowedOrigins,
		AdminMiddleware: a.Auth.Middleware,
		RegisterAdmin: func(mux *http.ServeMux) {
			a.Auth.Routes(mux)
			a.Brands.Routes(mux, a.Auth)
			teamH.Routes(mux)
		},
	})
}

// ServeHTTP runs the HTTP server until ctx is cancelled, then shuts down gracefully.
func (a *App) ServeHTTP(ctx context.Context) error {
	log := a.deps.Log
	srv := &http.Server{
		Addr:              a.deps.Config.HTTPAddr,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("http listening", "addr", srv.Addr)
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

// RunWorker works background jobs until ctx is cancelled.
func (a *App) RunWorker(ctx context.Context) error {
	log := a.deps.Log
	client, err := jobs.NewWorker(a.deps.Pool, log, jobs.Deps{Brands: a.Brands, Sessions: a.Store})
	if err != nil {
		return err
	}
	// Cancelling river's start context hard-stops it, so give it one that is
	// never cancelled and stop it gracefully below instead.
	if err := client.Start(context.WithoutCancel(ctx)); err != nil {
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

// Bootstrap returns an invite link for the first owner. It refuses once anyone
// has an account; after that, owners invite people from Team & access.
// Running it again (e.g. the link was lost) cancels the earlier link.
func (a *App) Bootstrap(ctx context.Context, email, name string) (string, error) {
	var link string
	err := a.Store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.LockUsers(ctx); err != nil {
			return err
		}
		n, err := tx.CountUsers(ctx)
		if err != nil {
			return err
		}
		if n > 0 {
			return errors.New("bootstrap is only for an empty database; invite people from Team & access instead")
		}
		if err := tx.DeleteOpenInvites(ctx); err != nil {
			return err
		}
		_, link, err = a.Auth.CreateInvite(ctx, tx, nil, auth.InviteInput{
			Email: email, Name: name, CompanyRole: store.CompanyOwner,
		})
		return err
	})
	return link, err
}

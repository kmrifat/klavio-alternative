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

	"github.com/kmrifat/klavio-alternative/api/internal/anomaly"
	"github.com/kmrifat/klavio-alternative/api/internal/approvals"
	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/blocklist"
	"github.com/kmrifat/klavio-alternative/api/internal/brands"
	"github.com/kmrifat/klavio-alternative/api/internal/cache"
	"github.com/kmrifat/klavio-alternative/api/internal/config"
	"github.com/kmrifat/klavio-alternative/api/internal/contacts"
	"github.com/kmrifat/klavio-alternative/api/internal/dnscheck"
	"github.com/kmrifat/klavio-alternative/api/internal/exports"
	"github.com/kmrifat/klavio-alternative/api/internal/health"
	"github.com/kmrifat/klavio-alternative/api/internal/imports"
	"github.com/kmrifat/klavio-alternative/api/internal/jobs"
	"github.com/kmrifat/klavio-alternative/api/internal/limits"
	"github.com/kmrifat/klavio-alternative/api/internal/postmaster"
	"github.com/kmrifat/klavio-alternative/api/internal/server"
	"github.com/kmrifat/klavio-alternative/api/internal/ses"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/team"
	"github.com/kmrifat/klavio-alternative/api/internal/templates"
	"github.com/kmrifat/klavio-alternative/api/internal/unsubscribe"
	"github.com/kmrifat/klavio-alternative/api/internal/warmup"
)

type Deps struct {
	Config     config.Config
	Log        *slog.Logger
	Pool       *pgxpool.Pool
	Redis      server.Pinger
	Ceremonies auth.CeremonyStore
	Access     *auth.AccessVerifier // nil = Cloudflare Access off
	SES        ses.Provisioner
	DNS        Resolver
	// ImportRunner overrides the river job runner (tests run imports inline).
	ImportRunner imports.Runner
	// TemplateRebuilder overrides the river job that recompiles templates
	// after a layout or allowlist change (tests run it inline).
	TemplateRebuilder templates.Rebuilder
	// Limiter rate-limits the public unsubscribe POSTs (Redis in production).
	Limiter cache.Limiter
	// Blocklists nil = blocklist checks off; Postmaster nil = not connected.
	Blocklists *blocklist.Checker
	Postmaster *postmaster.Client
	Version    string
}

// Resolver answers the DNS questions for brand checks and import MX checks.
type Resolver interface {
	dnscheck.Resolver
	imports.MXResolver
}

type App struct {
	deps      Deps
	Store     *store.Store
	Auth      *auth.Service
	Brands    *brands.Service
	Imports   *imports.Service
	Templates *templates.Service
	// Unsubscribe signs the unsubscribe/preferences links in every email.
	Unsubscribe *unsubscribe.Signer
	unsubSvc    *unsubscribe.Service
	// Approvals is the two-person rule; features Register their kinds on it.
	Approvals *approvals.Service
	Limits    *limits.Service
	Anomaly   *anomaly.Service
	exports   *exports.Service
	team      *team.Handlers
	// Warmup paces new brands' daily volume; the sending worker reserves from it.
	Warmup *warmup.Service
	Health *health.Service
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
	a := &App{
		deps:      d,
		Store:     st,
		Auth:      authSvc,
		Brands:    brands.NewService(st, d.SES, dnscheck.Checker{R: d.DNS}, d.Log),
		Imports:   imports.NewService(st, d.DNS, d.Log),
		Templates: templates.NewService(st, d.Log),
	}
	runner, rebuilder := d.ImportRunner, d.TemplateRebuilder
	if runner == nil || rebuilder == nil {
		client, err := jobs.NewInserter(d.Pool, d.Log)
		if err != nil {
			return nil, err
		}
		if runner == nil {
			runner = jobs.Runner{Client: client}
		}
		if rebuilder == nil {
			rebuilder = jobs.Runner{Client: client}
		}
	}
	a.Imports.SetRunner(runner)
	a.Templates.SetRebuilder(rebuilder)
	if a.Unsubscribe, err = unsubscribe.NewSigner(d.Config.UnsubscribeSecret, d.Config.UnsubscribeSecretOld); err != nil {
		return nil, err
	}
	a.unsubSvc = unsubscribe.NewService(st)

	// Governance (M5): approvals, safety limits, exports, anomaly alerts.
	a.Approvals = approvals.NewService(st, d.Log)
	a.Limits = limits.NewService(st, a.Approvals, d.Log)
	a.Anomaly = anomaly.NewService(st, d.Log)
	a.exports = exports.NewService(st, a.Approvals, a.Anomaly, d.Log)
	a.team = &team.Handlers{Store: st, Auth: authSvc, Approvals: a.Approvals, Log: d.Log}
	a.team.RegisterApprovals()
	authSvc.SetSignInGuard(a.Anomaly)
	a.Imports.SetLimits(a.Limits.Get)
	a.Imports.SetSpikeCheck(a.Anomaly.CheckImport)
	if a.deps.Limiter == nil {
		a.deps.Limiter = &cache.MemoryLimiter{}
	}

	// Health (M6): warm-up, health score, Safety center.
	a.Warmup = warmup.NewService(st)
	a.Health = health.NewService(st, d.SES, a.Brands, a.Warmup, health.Config{
		Blocklists:     d.Blocklists,
		BlocklistEvery: d.Config.BlocklistEvery,
		Postmaster:     d.Postmaster,
		AccessOn:       d.Access != nil,
	}, d.Log)
	return a, nil
}

// FromConfig builds the production dependencies: Redis-backed passkey
// ceremonies, Cloudflare Access when configured, real or fake SES, public DNS.
func FromConfig(ctx context.Context, cfg config.Config, log *slog.Logger, pool *pgxpool.Pool, rdb *cache.Client, version string) (*App, error) {
	d := Deps{
		Config: cfg, Log: log, Pool: pool, Redis: rdb, Version: version,
		Ceremonies: auth.RedisCeremonies{R: rdb.Client},
		Limiter:    rdb,
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
	if cfg.BlocklistResolver != "off" {
		d.Blocklists = &blocklist.Checker{R: blocklist.NewResolver(cfg.BlocklistResolver), DQSKey: cfg.SpamhausDQSKey}
	}
	pm, err := postmaster.New(ctx, cfg.PostmasterCredentialsFile)
	if err != nil {
		return nil, err
	}
	d.Postmaster = pm
	return New(d)
}

func (a *App) Handler() http.Handler {
	contactsH := &contacts.Handlers{Store: a.Store, Log: a.deps.Log}
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
			a.Templates.Routes(mux, a.Auth)
			a.team.Routes(mux)
			a.Approvals.Routes(mux, a.Auth)
			a.Limits.Routes(mux, a.Auth)
			a.exports.Routes(mux)
			contactsH.Routes(mux)
			a.Imports.Routes(mux, a.Auth)
			(&health.Handlers{Service: a.Health, Store: a.Store, Approvals: a.Approvals}).Routes(mux)
		},
		RegisterPublic: (&unsubscribe.Handlers{Signer: a.Unsubscribe, Service: a.unsubSvc, Limiter: a.deps.Limiter, Log: a.deps.Log}).Routes,
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
	client, err := jobs.NewWorker(a.deps.Pool, log, jobs.Deps{Brands: a.Brands, Sessions: a.Store, Imports: a.Imports, ImportFiles: a.Store, Templates: a.Templates, Governance: governanceJobs{a}, Health: a.Health})
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

// governanceJobs adapts the housekeeping job to the approvals engine and store.
type governanceJobs struct{ a *App }

func (g governanceJobs) ExpireApprovals(ctx context.Context) (int64, error) {
	return g.a.Approvals.Expire(ctx)
}
func (g governanceJobs) PurgeExpiredExports(ctx context.Context) (int64, error) {
	return g.a.Store.PurgeExpiredExports(ctx)
}

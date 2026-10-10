// Package jobs sets up the river job queue (stored in Postgres) and its workers.
package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

type Client = river.Client[pgx.Tx]

// Deps are the services jobs call into.
type Deps struct {
	Imports interface {
		RunCheck(ctx context.Context, brandID, importID uuid.UUID) error
		RunCommit(ctx context.Context, brandID, importID uuid.UUID) error
	}
	Brands interface {
		CheckAll(ctx context.Context) error
	}
	Sessions interface {
		DeleteExpiredSessions(ctx context.Context) (int64, error)
	}
	ImportFiles interface {
		PurgeAbandonedImports(ctx context.Context) (int64, error)
	}
	Governance interface {
		ExpireApprovals(ctx context.Context) (int64, error)
		PurgeExpiredExports(ctx context.Context) (int64, error)
	}
	Templates interface {
		RebuildBrand(ctx context.Context, brandID uuid.UUID) error
		RebuildStale(ctx context.Context) error
	}
	Health interface {
		RecomputeAll(ctx context.Context) error
		CheckAllBlocklists(ctx context.Context) error
		PullPostmaster(ctx context.Context) error
	}
	// Contacts API (M7): confirmation and re-permission emails, log cleanup.
	Opt interface {
		SendConfirmation(ctx context.Context, brandID, contactID uuid.UUID) error
		SendRepermissions(ctx context.Context) error
		Housekeeping(ctx context.Context) (int64, error)
	}
}

// HeartbeatArgs is a periodic no-op job that shows the worker is alive.
type HeartbeatArgs struct{}

func (HeartbeatArgs) Kind() string { return "heartbeat" }

type HeartbeatWorker struct {
	river.WorkerDefaults[HeartbeatArgs]
	Log *slog.Logger
}

func (w *HeartbeatWorker) Work(ctx context.Context, job *river.Job[HeartbeatArgs]) error {
	w.Log.DebugContext(ctx, "heartbeat", "job_id", job.ID)
	return nil
}

// DNSCheckArgs re-checks every brand's DNS and SES status (daily, S2).
type DNSCheckArgs struct{}

func (DNSCheckArgs) Kind() string { return "brands.dns_check" }

type DNSCheckWorker struct {
	river.WorkerDefaults[DNSCheckArgs]
	Deps Deps
}

func (w *DNSCheckWorker) Work(ctx context.Context, _ *river.Job[DNSCheckArgs]) error {
	return w.Deps.Brands.CheckAll(ctx)
}

// SessionCleanupArgs deletes expired sessions.
type SessionCleanupArgs struct{}

func (SessionCleanupArgs) Kind() string { return "auth.session_cleanup" }

type SessionCleanupWorker struct {
	river.WorkerDefaults[SessionCleanupArgs]
	Deps Deps
	Log  *slog.Logger
}

func (w *SessionCleanupWorker) Work(ctx context.Context, _ *river.Job[SessionCleanupArgs]) error {
	n, err := w.Deps.Sessions.DeleteExpiredSessions(ctx)
	if err == nil && n > 0 {
		w.Log.InfoContext(ctx, "deleted expired sessions", "count", n)
	}
	return err
}

// PurgeImportsArgs deletes uploaded files (personal data) of abandoned imports.
type PurgeImportsArgs struct{}

func (PurgeImportsArgs) Kind() string { return "imports.purge_abandoned" }

type PurgeImportsWorker struct {
	river.WorkerDefaults[PurgeImportsArgs]
	Deps Deps
	Log  *slog.Logger
}

func (w *PurgeImportsWorker) Work(ctx context.Context, _ *river.Job[PurgeImportsArgs]) error {
	n, err := w.Deps.ImportFiles.PurgeAbandonedImports(ctx)
	if err == nil && n > 0 {
		w.Log.InfoContext(ctx, "purged abandoned import files", "count", n)
	}
	return err
}

// GovernanceArgs expires overdue approval requests and deletes expired export files.
type GovernanceArgs struct{}

func (GovernanceArgs) Kind() string { return "governance.housekeeping" }

type GovernanceWorker struct {
	river.WorkerDefaults[GovernanceArgs]
	Deps Deps
	Log  *slog.Logger
}

func (w *GovernanceWorker) Work(ctx context.Context, _ *river.Job[GovernanceArgs]) error {
	n, err := w.Deps.Governance.ExpireApprovals(ctx)
	if err != nil {
		return err
	}
	m, err := w.Deps.Governance.PurgeExpiredExports(ctx)
	if n > 0 || m > 0 {
		w.Log.InfoContext(ctx, "governance housekeeping", "approvals_expired", n, "export_files_deleted", m)
	}
	return err
}

// ImportCheckArgs runs an import's safety check (S4 step 3).
type ImportCheckArgs struct {
	BrandID  uuid.UUID `json:"brand_id"`
	ImportID uuid.UUID `json:"import_id"`
}

func (ImportCheckArgs) Kind() string { return "imports.check" }

type ImportCheckWorker struct {
	river.WorkerDefaults[ImportCheckArgs]
	Deps Deps
}

func (w *ImportCheckWorker) Work(ctx context.Context, j *river.Job[ImportCheckArgs]) error {
	return w.Deps.Imports.RunCheck(ctx, j.Args.BrandID, j.Args.ImportID)
}

// ImportCommitArgs imports the checked rows (S4 step 4).
type ImportCommitArgs struct {
	BrandID  uuid.UUID `json:"brand_id"`
	ImportID uuid.UUID `json:"import_id"`
}

func (ImportCommitArgs) Kind() string { return "imports.commit" }

type ImportCommitWorker struct {
	river.WorkerDefaults[ImportCommitArgs]
	Deps Deps
}

func (w *ImportCommitWorker) Work(ctx context.Context, j *river.Job[ImportCommitArgs]) error {
	return w.Deps.Imports.RunCommit(ctx, j.Args.BrandID, j.Args.ImportID)
}

// Timeout: big files with many domains take a while to check.
func (w *ImportCommitWorker) Timeout(*river.Job[ImportCommitArgs]) time.Duration {
	return 30 * time.Minute
}
func (w *ImportCheckWorker) Timeout(*river.Job[ImportCheckArgs]) time.Duration {
	return 30 * time.Minute
}

// TemplateRebuildArgs recompiles a brand's templates after its layout or
// link allowlist changed.
type TemplateRebuildArgs struct {
	BrandID uuid.UUID `json:"brand_id"`
}

func (TemplateRebuildArgs) Kind() string { return "templates.rebuild" }

type TemplateRebuildWorker struct {
	river.WorkerDefaults[TemplateRebuildArgs]
	Deps Deps
}

func (w *TemplateRebuildWorker) Work(ctx context.Context, j *river.Job[TemplateRebuildArgs]) error {
	return w.Deps.Templates.RebuildBrand(ctx, j.Args.BrandID)
}

func (w *TemplateRebuildWorker) Timeout(*river.Job[TemplateRebuildArgs]) time.Duration {
	return 30 * time.Minute
}

// TemplateSweepArgs rebuilds any brand whose templates are still stale, in
// case a rebuild job was lost.
type TemplateSweepArgs struct{}

func (TemplateSweepArgs) Kind() string { return "templates.sweep" }

type TemplateSweepWorker struct {
	river.WorkerDefaults[TemplateSweepArgs]
	Deps Deps
}

func (w *TemplateSweepWorker) Work(ctx context.Context, _ *river.Job[TemplateSweepArgs]) error {
	return w.Deps.Templates.RebuildStale(ctx)
}

func (w *TemplateSweepWorker) Timeout(*river.Job[TemplateSweepArgs]) time.Duration {
	return 30 * time.Minute
}

// HealthScoreArgs recomputes every brand's health score (hourly, S1/S2).
type HealthScoreArgs struct{}

func (HealthScoreArgs) Kind() string { return "health.recompute" }

type HealthScoreWorker struct {
	river.WorkerDefaults[HealthScoreArgs]
	Deps Deps
}

func (w *HealthScoreWorker) Work(ctx context.Context, _ *river.Job[HealthScoreArgs]) error {
	return w.Deps.Health.RecomputeAll(ctx)
}

// BlocklistArgs re-checks sending and link domains against the blocklists.
// It runs hourly but each brand is only looked up again once its results
// are older than BLOCKLIST_CHECK_INTERVAL.
type BlocklistArgs struct{}

func (BlocklistArgs) Kind() string { return "health.blocklists" }

type BlocklistWorker struct {
	river.WorkerDefaults[BlocklistArgs]
	Deps Deps
}

func (w *BlocklistWorker) Work(ctx context.Context, _ *river.Job[BlocklistArgs]) error {
	return w.Deps.Health.CheckAllBlocklists(ctx)
}

// PostmasterArgs pulls Gmail Postmaster Tools stats (daily).
type PostmasterArgs struct{}

func (PostmasterArgs) Kind() string { return "health.postmaster" }

type PostmasterWorker struct {
	river.WorkerDefaults[PostmasterArgs]
	Deps Deps
}

func (w *PostmasterWorker) Work(ctx context.Context, _ *river.Job[PostmasterArgs]) error {
	return w.Deps.Health.PullPostmaster(ctx)
}

// ConfirmationArgs sends one double opt-in email (P2).
type ConfirmationArgs struct {
	BrandID   uuid.UUID `json:"brand_id"`
	ContactID uuid.UUID `json:"contact_id"`
}

func (ConfirmationArgs) Kind() string { return "contacts.confirmation_email" }

type ConfirmationWorker struct {
	river.WorkerDefaults[ConfirmationArgs]
	Deps Deps
}

func (w *ConfirmationWorker) Work(ctx context.Context, j *river.Job[ConfirmationArgs]) error {
	return w.Deps.Opt.SendConfirmation(ctx, j.Args.BrandID, j.Args.ContactID)
}

// RepermissionArgs sends re-permission emails to quarantined imports (hourly,
// up to the daily limit).
type RepermissionArgs struct{}

func (RepermissionArgs) Kind() string { return "contacts.repermission" }

type RepermissionWorker struct {
	river.WorkerDefaults[RepermissionArgs]
	Deps Deps
}

func (w *RepermissionWorker) Work(ctx context.Context, _ *river.Job[RepermissionArgs]) error {
	return w.Deps.Opt.SendRepermissions(ctx)
}

// APIHousekeepingArgs forgets old API call logs, idempotency records and
// frequency-cap history.
type APIHousekeepingArgs struct{}

func (APIHousekeepingArgs) Kind() string { return "contacts.housekeeping" }

type APIHousekeepingWorker struct {
	river.WorkerDefaults[APIHousekeepingArgs]
	Deps Deps
	Log  *slog.Logger
}

func (w *APIHousekeepingWorker) Work(ctx context.Context, _ *river.Job[APIHousekeepingArgs]) error {
	n, err := w.Deps.Opt.Housekeeping(ctx)
	if err == nil && n > 0 {
		w.Log.InfoContext(ctx, "contacts API housekeeping", "rows_deleted", n)
	}
	return err
}

// Runner queues import jobs, template rebuilds and confirmation emails; it
// satisfies imports.Runner, templates.Rebuilder and contactsapi.ConfirmationQueue.
type Runner struct{ Client *Client }

func (r Runner) StartCheck(ctx context.Context, brandID, importID uuid.UUID) error {
	_, err := r.Client.Insert(ctx, ImportCheckArgs{BrandID: brandID, ImportID: importID}, &river.InsertOpts{MaxAttempts: 3})
	return err
}

func (r Runner) StartCommit(ctx context.Context, brandID, importID uuid.UUID) error {
	_, err := r.Client.Insert(ctx, ImportCommitArgs{BrandID: brandID, ImportID: importID}, &river.InsertOpts{MaxAttempts: 3})
	return err
}

func (r Runner) QueueConfirmation(ctx context.Context, brandID, contactID uuid.UUID) error {
	_, err := r.Client.Insert(ctx, ConfirmationArgs{BrandID: brandID, ContactID: contactID}, &river.InsertOpts{MaxAttempts: 5})
	return err
}

func (r Runner) QueueRebuild(ctx context.Context, brandID uuid.UUID) error {
	_, err := r.Client.Insert(ctx, TemplateRebuildArgs{BrandID: brandID}, &river.InsertOpts{MaxAttempts: 10})
	return err
}

// NewInserter returns a client that only queues jobs (the API process).
func NewInserter(pool *pgxpool.Pool, log *slog.Logger) (*Client, error) {
	return river.NewClient(riverpgxv5.New(pool), &river.Config{Logger: log})
}

func periodic(every time.Duration, args river.JobArgs, runOnStart bool) *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(every),
		func() (river.JobArgs, *river.InsertOpts) { return args, nil },
		&river.PeriodicJobOpts{RunOnStart: runOnStart},
	)
}

// NewWorker returns a client that works jobs. The worker process uses this.
func NewWorker(pool *pgxpool.Pool, log *slog.Logger, deps Deps) (*Client, error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &HeartbeatWorker{Log: log})
	jobs := []*river.PeriodicJob{periodic(5*time.Minute, HeartbeatArgs{}, true)}

	if deps.Brands != nil {
		river.AddWorker(workers, &DNSCheckWorker{Deps: deps})
		jobs = append(jobs, periodic(24*time.Hour, DNSCheckArgs{}, false))
	}
	if deps.Imports != nil {
		river.AddWorker(workers, &ImportCheckWorker{Deps: deps})
		river.AddWorker(workers, &ImportCommitWorker{Deps: deps})
	}
	if deps.Governance != nil {
		river.AddWorker(workers, &GovernanceWorker{Deps: deps, Log: log})
		jobs = append(jobs, periodic(15*time.Minute, GovernanceArgs{}, true))
	}
	if deps.ImportFiles != nil {
		river.AddWorker(workers, &PurgeImportsWorker{Deps: deps, Log: log})
		jobs = append(jobs, periodic(time.Hour, PurgeImportsArgs{}, true))
	}
	if deps.Templates != nil {
		river.AddWorker(workers, &TemplateRebuildWorker{Deps: deps})
		river.AddWorker(workers, &TemplateSweepWorker{Deps: deps})
		jobs = append(jobs, periodic(10*time.Minute, TemplateSweepArgs{}, true))
	}
	if deps.Health != nil {
		river.AddWorker(workers, &HealthScoreWorker{Deps: deps})
		river.AddWorker(workers, &BlocklistWorker{Deps: deps})
		river.AddWorker(workers, &PostmasterWorker{Deps: deps})
		jobs = append(jobs,
			periodic(time.Hour, HealthScoreArgs{}, true),
			periodic(time.Hour, BlocklistArgs{}, true),
			periodic(24*time.Hour, PostmasterArgs{}, true))
	}
	if deps.Opt != nil {
		river.AddWorker(workers, &ConfirmationWorker{Deps: deps})
		river.AddWorker(workers, &RepermissionWorker{Deps: deps})
		river.AddWorker(workers, &APIHousekeepingWorker{Deps: deps, Log: log})
		jobs = append(jobs, periodic(time.Hour, RepermissionArgs{}, true), periodic(time.Hour, APIHousekeepingArgs{}, true))
	}
	if deps.Sessions != nil {
		river.AddWorker(workers, &SessionCleanupWorker{Deps: deps, Log: log})
		jobs = append(jobs, periodic(time.Hour, SessionCleanupArgs{}, true))
	}

	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger:       log,
		Workers:      workers,
		Queues:       map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 10}},
		PeriodicJobs: jobs,
	})
}

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
	Campaigns interface {
		Step(ctx context.Context, campaignID uuid.UUID) error
		Sweep(ctx context.Context) error
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

// CampaignStepArgs runs a campaign's next step: start, a send batch, or the
// end of the test-slice hold.
type CampaignStepArgs struct {
	CampaignID uuid.UUID `json:"campaign_id"`
}

func (CampaignStepArgs) Kind() string { return "campaigns.step" }

type CampaignStepWorker struct {
	river.WorkerDefaults[CampaignStepArgs]
	Deps Deps
}

func (w *CampaignStepWorker) Work(ctx context.Context, j *river.Job[CampaignStepArgs]) error {
	return w.Deps.Campaigns.Step(ctx, j.Args.CampaignID)
}

// CampaignSweepArgs runs any campaign step that is due, in case a queued
// step was lost.
type CampaignSweepArgs struct{}

func (CampaignSweepArgs) Kind() string { return "campaigns.sweep" }

type CampaignSweepWorker struct {
	river.WorkerDefaults[CampaignSweepArgs]
	Deps Deps
}

func (w *CampaignSweepWorker) Work(ctx context.Context, _ *river.Job[CampaignSweepArgs]) error {
	return w.Deps.Campaigns.Sweep(ctx)
}

func (w *CampaignSweepWorker) Timeout(*river.Job[CampaignSweepArgs]) time.Duration {
	return 10 * time.Minute
}

// Runner queues import jobs, template rebuilds and campaign steps; it
// satisfies imports.Runner, templates.Rebuilder and campaigns.Runner.
type Runner struct{ Client *Client }

func (r Runner) StartCheck(ctx context.Context, brandID, importID uuid.UUID) error {
	_, err := r.Client.Insert(ctx, ImportCheckArgs{BrandID: brandID, ImportID: importID}, &river.InsertOpts{MaxAttempts: 3})
	return err
}

func (r Runner) StartCommit(ctx context.Context, brandID, importID uuid.UUID) error {
	_, err := r.Client.Insert(ctx, ImportCommitArgs{BrandID: brandID, ImportID: importID}, &river.InsertOpts{MaxAttempts: 3})
	return err
}

func (r Runner) QueueCampaignStep(ctx context.Context, campaignID uuid.UUID, at time.Time) error {
	_, err := r.Client.Insert(ctx, CampaignStepArgs{CampaignID: campaignID}, &river.InsertOpts{ScheduledAt: at, MaxAttempts: 5})
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
	if deps.Campaigns != nil {
		river.AddWorker(workers, &CampaignStepWorker{Deps: deps})
		river.AddWorker(workers, &CampaignSweepWorker{Deps: deps})
		jobs = append(jobs, periodic(time.Minute, CampaignSweepArgs{}, true))
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

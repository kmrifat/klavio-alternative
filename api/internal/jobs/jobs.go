// Package jobs sets up the river job queue (stored in Postgres) and its workers.
package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

type Client = river.Client[pgx.Tx]

// Deps are the services jobs call into.
type Deps struct {
	Brands interface {
		CheckAll(ctx context.Context) error
	}
	Sessions interface {
		DeleteExpiredSessions(ctx context.Context) (int64, error)
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

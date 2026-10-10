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

// HeartbeatArgs is a periodic no-op job. It proves the worker is alive and the
// queue is being drained; real jobs (sending, SES events, brakes) arrive in M3/M4.
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

// NewWorker returns a client that works jobs. The worker process uses this.
func NewWorker(pool *pgxpool.Pool, log *slog.Logger) (*Client, error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &HeartbeatWorker{Log: log})

	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger:  log,
		Workers: workers,
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: 10},
		},
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(
				river.PeriodicInterval(5*time.Minute),
				func() (river.JobArgs, *river.InsertOpts) { return HeartbeatArgs{}, nil },
				&river.PeriodicJobOpts{RunOnStart: true},
			),
		},
	})
}

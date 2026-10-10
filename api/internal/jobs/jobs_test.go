package jobs_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/kmrifat/klavio-alternative/api/internal/db"
	"github.com/kmrifat/klavio-alternative/api/internal/jobs"
	"github.com/kmrifat/klavio-alternative/api/internal/testdb"
)

// TestMigrateAndWorkJob needs a real Postgres. It runs in CI and with
// `make test-integration` locally; otherwise it is skipped.
func TestMigrateAndWorkJob(t *testing.T) {
	// Its own schema, like every other DB test: River finds its tables via
	// search_path, so migrating into public would leak into the test schemas.
	pool := testdb.New(t) // migrates once
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Running migrations again must be a no-op.
	if err := db.Migrate(ctx, pool, log); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	var hasCitext bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'citext')`).Scan(&hasCitext); err != nil {
		t.Fatal(err)
	}
	if !hasCitext {
		t.Error("citext extension missing after migrate")
	}

	client, err := jobs.NewWorker(pool, log, jobs.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	completed, unsubscribe := client.Subscribe(river.EventKindJobCompleted)
	defer unsubscribe()

	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Stop(context.Background()) }()

	// The periodic heartbeat runs on start; inserting one more proves inserts work too.
	if _, err := client.Insert(ctx, jobs.HeartbeatArgs{}, nil); err != nil {
		t.Fatal(err)
	}

	for seen := 0; seen < 2; {
		select {
		case ev := <-completed:
			if ev.Job.Kind == (jobs.HeartbeatArgs{}).Kind() {
				seen++
			}
		case <-ctx.Done():
			t.Fatal("timed out waiting for heartbeat jobs to complete")
		}
	}
}

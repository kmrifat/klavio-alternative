package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/kmrifat/klavio-alternative/api/internal/confirm"
)

type gatedOpt struct {
	closed bool
	sent   int
}

func (g *gatedOpt) SendConfirmation(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Time) error {
	if g.closed {
		return fmt.Errorf("%w: test brake", confirm.ErrGateClosed)
	}
	g.sent++
	return nil
}
func (g *gatedOpt) SendRepermissions(context.Context) error     { return nil }
func (g *gatedOpt) Housekeeping(context.Context) (int64, error) { return 0, nil }

func job(queued time.Time) *river.Job[ConfirmationArgs] {
	return &river.Job[ConfirmationArgs]{JobRow: &rivertype.JobRow{CreatedAt: queued}, Args: ConfirmationArgs{QueuedAt: queued}}
}

func TestBlockedConfirmationWaitsThenExpires(t *testing.T) {
	opt := &gatedOpt{closed: true}
	w := &ConfirmationWorker{Deps: Deps{Opt: opt}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := context.Background()

	// Held back: snoozed (no attempt used), longer the longer it has waited.
	var snooze *rivertype.JobSnoozeError
	err := w.Work(ctx, job(time.Now().Add(-10*time.Second)))
	if !errors.As(err, &snooze) || snooze.Duration != time.Minute {
		t.Fatalf("fresh: %v", err)
	}
	if err := w.Work(ctx, job(time.Now().Add(-10*time.Hour))); !errors.As(err, &snooze) || snooze.Duration != 30*time.Minute {
		t.Fatalf("after 10 hours: %v", err)
	}
	// Past 48 hours: dropped (logged), not retried for ever.
	if err := w.Work(ctx, job(time.Now().Add(-49*time.Hour))); err != nil {
		t.Fatalf("expired: %v", err)
	}
	// The gate opens: sent, once.
	opt.closed = false
	if err := w.Work(ctx, job(time.Now().Add(-time.Hour))); err != nil || opt.sent != 1 {
		t.Fatalf("after the gate opened: %v, sent %d", err, opt.sent)
	}
}

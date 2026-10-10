package events

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Reader long-polls the queue and processes each message. A message is
// deleted once handled (or when it isn't an SES event at all); one that
// fails stays, comes back after the visibility timeout, and after 5 tries
// SQS moves it to the dead-letter queue.
type Reader struct {
	Queue     Queue
	Processor *Processor
	Log       *slog.Logger
}

// Run reads until ctx is cancelled.
func (r *Reader) Run(ctx context.Context) error {
	r.Log.InfoContext(ctx, "SES event reader started")
	for ctx.Err() == nil {
		if _, err := r.Once(ctx); err != nil && ctx.Err() == nil {
			r.Log.WarnContext(ctx, "reading SES events", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}
	return nil
}

// Once handles one batch; returns how many messages it handled.
func (r *Reader) Once(ctx context.Context) (int, error) {
	msgs, err := r.Queue.Receive(ctx, 10)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, m := range msgs {
		// Finish a message even while shutting down: half-applied is worse.
		mctx := context.WithoutCancel(ctx)
		e, err := Parse(m.Body)
		switch {
		case errors.Is(err, ErrNotSES):
			r.Log.WarnContext(ctx, "dropping a non-SES message", "message_id", m.ID)
		case err != nil:
			r.Log.ErrorContext(ctx, "unreadable SES event (it goes to the dead-letter queue after 5 tries)",
				"message_id", m.ID, "receive_count", m.ReceiveCount, "err", err)
			continue
		default:
			if _, err := r.Processor.Process(mctx, e); err != nil {
				r.Log.ErrorContext(ctx, "processing SES event", "message_id", m.ID, "event_id", e.ID,
					"receive_count", m.ReceiveCount, "err", err)
				continue
			}
		}
		if err := r.Queue.Delete(mctx, m.Receipt); err != nil {
			r.Log.WarnContext(ctx, "deleting SQS message (it'll be processed again, harmlessly)", "message_id", m.ID, "err", err)
			continue
		}
		done++
	}
	return done, nil
}

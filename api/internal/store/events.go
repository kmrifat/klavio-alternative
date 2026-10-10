package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// --- SES events (M4) ---

func (s *Store) BrandByConfigurationSet(ctx context.Context, name string) (Brand, error) {
	rows, err := s.db.Query(ctx, `SELECT `+brandCols+` FROM brands WHERE ses_configuration_set = $1`, name)
	return one[Brand](rows, err)
}

type EmailEvent struct {
	EventID      string
	Type         string
	Subtype      string
	BrandID      *uuid.UUID
	SESMessageID string
	SendID       *int64
	EmailHash    []byte // nil = not about one recipient
	OccurredAt   time.Time
	Detail       json.RawMessage
}

// InsertEmailEvent stores an event row; false when it was stored before
// (SQS delivered the message again).
func (s *Store) InsertEmailEvent(ctx context.Context, e EmailEvent) (bool, error) {
	if e.EmailHash == nil {
		e.EmailHash = []byte{}
	}
	if len(e.Detail) == 0 {
		e.Detail = json.RawMessage(`{}`)
	}
	tag, err := s.db.Exec(ctx, `
		INSERT INTO email_events (event_id, type, subtype, brand_id, ses_message_id, send_id, email_hash, occurred_at, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT (event_id, email_hash) DO NOTHING`,
		e.EventID, e.Type, e.Subtype, e.BrandID, e.SESMessageID, e.SendID, e.EmailHash, e.OccurredAt, e.Detail)
	return tag.RowsAffected() == 1, err
}

// SoftBouncesSince counts transient bounces that count against the person
// (not ones caused by our message: too large, content or attachment rejected).
func (s *Store) SoftBouncesSince(ctx context.Context, emailHash []byte, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `
		SELECT count(*) FROM email_events
		WHERE email_hash = $1 AND type = 'bounce' AND occurred_at > $2
			AND subtype NOT LIKE 'permanent/%'
			AND subtype NOT IN ('transient/MessageTooLarge', 'transient/ContentRejected', 'transient/AttachmentRejected')`,
		emailHash, since).Scan(&n)
	return n, err
}

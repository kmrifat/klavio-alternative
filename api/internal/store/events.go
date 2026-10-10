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
	FeedbackID   string // SES's bounce/complaint id ("" = none)
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
// (SQS delivered the message again, or the same notification came under
// another EventBridge id: same feedback id, or a delivery already recorded).
func (s *Store) InsertEmailEvent(ctx context.Context, e EmailEvent) (bool, error) {
	if e.EmailHash == nil {
		e.EmailHash = []byte{}
	}
	if len(e.Detail) == 0 {
		e.Detail = json.RawMessage(`{}`)
	}
	tag, err := s.db.Exec(ctx, `
		INSERT INTO email_events (event_id, feedback_id, type, subtype, brand_id, ses_message_id, send_id, email_hash, occurred_at, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT DO NOTHING`,
		e.EventID, e.FeedbackID, e.Type, e.Subtype, e.BrandID, e.SESMessageID, e.SendID, e.EmailHash, e.OccurredAt, e.Detail)
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

// SendRef is who and what a send was.
type SendRef struct {
	ID         int64     `db:"id"`
	CampaignID uuid.UUID `db:"campaign_id"`
	BrandID    uuid.UUID `db:"brand_id"`
	ContactID  uuid.UUID `db:"contact_id"`
}

// SendByID: ErrNotFound for a send that isn't ours (or was deleted).
func (s *Store) SendByID(ctx context.Context, id int64) (SendRef, error) {
	rows, err := s.db.Query(ctx, `SELECT id, campaign_id, brand_id, contact_id FROM sends WHERE id = $1`, id)
	return one[SendRef](rows, err)
}

// CampaignFeedback: what happened to a campaign's emails so far.
func (s *Store) CampaignFeedback(ctx context.Context, campaignID uuid.UUID) (FeedbackCounts, error) {
	var c FeedbackCounts
	err := s.db.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE e.type = 'delivery'),
		       count(*) FILTER (WHERE e.type = 'bounce' AND e.subtype LIKE 'permanent/%'),
		       count(*) FILTER (WHERE e.type = 'complaint')
		FROM sends s JOIN email_events e ON e.send_id = s.id WHERE s.campaign_id = $1`, campaignID).
		Scan(&c.Delivered, &c.Bounces, &c.Complaints)
	if err != nil {
		return c, err
	}
	err = s.db.QueryRow(ctx, `SELECT count(DISTINCT contact_id) FROM consent_records WHERE campaign_id = $1 AND kind = 'unsubscribed'`,
		campaignID).Scan(&c.Unsubscribes)
	return c, err
}

package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// --- brakes (M4) ---

// FeedbackCounts: deliveries, hard bounces and complaints from SES events,
// and unsubscribes from consent records, since a time — for one brand, or
// all brands when brandID is nil.
type FeedbackCounts struct {
	Delivered    int
	Bounces      int
	Complaints   int
	Unsubscribes int
}

func (s *Store) FeedbackCounts(ctx context.Context, brandID *uuid.UUID, since time.Time) (FeedbackCounts, error) {
	var c FeedbackCounts
	err := s.db.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE type = 'delivery'),
		       count(*) FILTER (WHERE type = 'bounce' AND subtype LIKE 'permanent/%'),
		       count(*) FILTER (WHERE type = 'complaint')
		FROM email_events WHERE occurred_at > $2 AND ($1::uuid IS NULL OR brand_id = $1)`, brandID, since).
		Scan(&c.Delivered, &c.Bounces, &c.Complaints)
	if err != nil {
		return c, err
	}
	err = s.db.QueryRow(ctx, `
		SELECT count(*) FROM consent_records
		WHERE kind = 'unsubscribed' AND method IN ('link', 'header') AND occurred_at > $2 AND ($1::uuid IS NULL OR brand_id = $1)`,
		brandID, since).Scan(&c.Unsubscribes)
	return c, err
}

// SetBrakeState changes a brand's brake; paused also stops the brand.
func (s *Store) SetBrakeState(ctx context.Context, brandID uuid.UUID, state string) error {
	return expectOne(s.db.Exec(ctx, `
		UPDATE brands SET brake_state = $2, status = CASE WHEN $2 = 'paused' THEN 'paused' ELSE status END, updated_at = now()
		WHERE id = $1`, brandID, state))
}

type SafetyEvent struct {
	ID         int64           `db:"id" json:"id"`
	At         time.Time       `db:"at" json:"at"`
	Scope      string          `db:"scope" json:"scope"`
	BrandID    *uuid.UUID      `db:"brand_id" json:"brand_id"`
	CampaignID *uuid.UUID      `db:"campaign_id" json:"campaign_id"`
	Action     string          `db:"action" json:"action"`
	FromState  string          `db:"from_state" json:"from_state"`
	ToState    string          `db:"to_state" json:"to_state"`
	Reason     string          `db:"reason" json:"reason"`
	Metrics    json.RawMessage `db:"metrics" json:"metrics"`
}

func (s *Store) AddSafetyEvent(ctx context.Context, e SafetyEvent, metrics any) error {
	m, err := json.Marshal(metrics)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO safety_events (scope, brand_id, campaign_id, action, from_state, to_state, reason, metrics)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, e.Scope, e.BrandID, e.CampaignID, e.Action, e.FromState, e.ToState, e.Reason, m)
	return err
}

func (s *Store) SafetyEvents(ctx context.Context, brandID uuid.UUID, limit int) ([]SafetyEvent, error) {
	rows, err := s.db.Query(ctx, `SELECT id, at, scope, brand_id, campaign_id, action, from_state, to_state, reason, metrics
		FROM safety_events WHERE brand_id = $1 OR (scope = 'account' AND brand_id IS NULL) ORDER BY at DESC, id DESC LIMIT $2`, brandID, limit)
	return all[SafetyEvent](rows, err)
}

// AccountBrake: when sending was stopped for every brand (nil = it isn't).
func (s *Store) AccountBrake(ctx context.Context) (*time.Time, string, error) {
	var at *time.Time
	var reason string
	err := s.db.QueryRow(ctx, `SELECT account_brake_at, account_brake_reason FROM company_state WHERE id = 1`).Scan(&at, &reason)
	if translate(err) == ErrNotFound {
		return nil, "", nil
	}
	return at, reason, err
}

// SetAccountBrake stops every brand; true if it wasn't stopped already.
func (s *Store) SetAccountBrake(ctx context.Context, reason string) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		INSERT INTO company_state (id, account_brake_at, account_brake_reason) VALUES (1, now(), $1)
		ON CONFLICT (id) DO UPDATE SET account_brake_at = now(), account_brake_reason = $1
		WHERE company_state.account_brake_at IS NULL`, reason)
	return tag.RowsAffected() == 1, err
}

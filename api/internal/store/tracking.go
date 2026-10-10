package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// --- clicks and opens (M4) ---

type Click struct {
	SendID    int64
	BrandID   uuid.UUID
	ContactID uuid.UUID
	LinkNo    int
	URL       string
	At        time.Time
	Method    string
	IP        string
	UserAgent string
	IsBot     bool
	BotReason string
}

func (s *Store) InsertClick(ctx context.Context, c Click) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `
		INSERT INTO clicks (send_id, brand_id, contact_id, link_no, url, at, method, ip, user_agent, is_bot, bot_reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id`,
		c.SendID, c.BrandID, c.ContactID, c.LinkNo, c.URL, c.At, c.Method, c.IP, c.UserAgent, c.IsBot, c.BotReason).Scan(&id)
	return id, err
}

type PriorClick struct {
	ID     int64     `db:"id"`
	At     time.Time `db:"at"`
	LinkNo int       `db:"link_no"`
	IsBot  bool      `db:"is_bot"`
}

// ClicksSince: the email's clicks from `since` on.
func (s *Store) ClicksSince(ctx context.Context, sendID int64, since time.Time) ([]PriorClick, error) {
	rows, err := s.db.Query(ctx, `SELECT id, at, link_no, is_bot FROM clicks WHERE send_id = $1 AND at >= $2 ORDER BY at`, sendID, since)
	return all[PriorClick](rows, err)
}

func (s *Store) MarkClicksBot(ctx context.Context, ids []int64, reason string) error {
	_, err := s.db.Exec(ctx, `UPDATE clicks SET is_bot = true, bot_reason = $2 WHERE id = ANY($1) AND NOT is_bot`, ids, reason)
	return err
}

// LockSendClicks serialises recording clicks of one email (the burst check
// looks at the others).
func (s *Store) LockSendClicks(ctx context.Context, sendID int64) error {
	_, err := s.db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('clicks:' || $1::bigint::text, 0))`, sendID)
	return err
}

// DeliveredAt: when SES delivered the email (nil = no delivery event yet).
func (s *Store) DeliveredAt(ctx context.Context, sendID int64) (*time.Time, error) {
	var t *time.Time
	err := s.db.QueryRow(ctx, `SELECT min(occurred_at) FROM email_events WHERE send_id = $1 AND type = 'delivery'`, sendID).Scan(&t)
	return t, err
}

// RecordEngagement: a person clicked. It's the sign of life engagement
// tiers are built on (only contacts who allowed tracking get here).
func (s *Store) RecordEngagement(ctx context.Context, brandID, contactID uuid.UUID, at time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE contacts SET last_click_at = GREATEST(last_click_at, $3), last_engaged_at = GREATEST(last_engaged_at, $3)
		WHERE brand_id = $1 AND id = $2 AND tracking_allowed`, brandID, contactID, at)
	return err
}

type Open struct {
	SendID    int64
	BrandID   uuid.UUID
	ContactID uuid.UUID
	At        time.Time
	IP        string
	UserAgent string
	IsMachine bool
	Reason    string
}

func (s *Store) InsertOpen(ctx context.Context, o Open) error {
	_, err := s.db.Exec(ctx, `INSERT INTO opens (send_id, brand_id, contact_id, at, ip, user_agent, is_machine, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, o.SendID, o.BrandID, o.ContactID, o.At, o.IP, o.UserAgent, o.IsMachine, o.Reason)
	return err
}

package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// --- warm-up ---

// WarmupStart returns the day the brand first sent (nil = not started).
func (s *Store) WarmupStart(ctx context.Context, brandID uuid.UUID) (*time.Time, error) {
	var d *time.Time
	err := s.db.QueryRow(ctx, `SELECT warmup_started_on FROM brands WHERE id = $1`, brandID).Scan(&d)
	return d, translate(err)
}

// RecordSends adds to the brand's count for a UTC day and starts the warm-up
// on the first send. Returns the day's new total.
func (s *Store) RecordSends(ctx context.Context, brandID uuid.UUID, day time.Time, n int) (int, error) {
	if _, err := s.db.Exec(ctx, `UPDATE brands SET warmup_started_on = $2 WHERE id = $1 AND warmup_started_on IS NULL`, brandID, day); err != nil {
		return 0, err
	}
	var total int
	err := s.db.QueryRow(ctx, `
		INSERT INTO brand_daily_sends (brand_id, day, sent) VALUES ($1, $2, $3)
		ON CONFLICT (brand_id, day) DO UPDATE SET sent = brand_daily_sends.sent + EXCLUDED.sent
		RETURNING sent`, brandID, day, n).Scan(&total)
	return total, err
}

func (s *Store) SentOn(ctx context.Context, brandID uuid.UUID, day time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT COALESCE((SELECT sent FROM brand_daily_sends WHERE brand_id = $1 AND day = $2), 0)`, brandID, day).Scan(&n)
	return n, err
}

// SentAllBrandsOn: marketing sends across brands on a UTC day (account meter).
func (s *Store) SentAllBrandsOn(ctx context.Context, day time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT COALESCE(sum(sent), 0) FROM brand_daily_sends WHERE day = $1`, day).Scan(&n)
	return n, err
}

// --- health scores ---

type BrandHealth struct {
	BrandID    uuid.UUID       `db:"brand_id" json:"brand_id"`
	Score      int             `db:"score" json:"score"`
	Components json.RawMessage `db:"components" json:"components"`
	ComputedAt time.Time       `db:"computed_at" json:"computed_at"`
}

func (s *Store) SaveHealth(ctx context.Context, brandID uuid.UUID, score int, components any) error {
	c, err := json.Marshal(components)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO brand_health (brand_id, score, components, computed_at) VALUES ($1, $2, $3, now())
		ON CONFLICT (brand_id) DO UPDATE SET score = EXCLUDED.score, components = EXCLUDED.components, computed_at = now()`,
		brandID, score, c)
	return err
}

func (s *Store) AllHealth(ctx context.Context) (map[uuid.UUID]BrandHealth, error) {
	rows, err := s.db.Query(ctx, `SELECT brand_id, score, components, computed_at FROM brand_health`)
	list, err := all[BrandHealth](rows, err)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]BrandHealth, len(list))
	for _, h := range list {
		out[h.BrandID] = h
	}
	return out, nil
}

// --- blocklists ---

type BlocklistResult struct {
	BrandID   uuid.UUID `db:"brand_id" json:"-"`
	Zone      string    `db:"zone" json:"zone"`
	Domain    string    `db:"domain" json:"domain"`
	Status    string    `db:"status" json:"status"`
	Detail    string    `db:"detail" json:"detail"`
	CheckedAt time.Time `db:"checked_at" json:"checked_at"`
}

func (s *Store) SaveBlocklistResult(ctx context.Context, r BlocklistResult) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO blocklist_results (brand_id, zone, domain, status, detail, checked_at) VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (brand_id, zone, domain) DO UPDATE SET status = EXCLUDED.status, detail = EXCLUDED.detail, checked_at = now()`,
		r.BrandID, r.Zone, r.Domain, r.Status, r.Detail)
	return err
}

func (s *Store) BlocklistResults(ctx context.Context, brandID uuid.UUID) ([]BlocklistResult, error) {
	rows, err := s.db.Query(ctx, `SELECT brand_id, zone, domain, status, detail, checked_at FROM blocklist_results
		WHERE brand_id = $1 ORDER BY domain, zone`, brandID)
	return all[BlocklistResult](rows, err)
}

// --- Google Postmaster Tools ---

type PostmasterStat struct {
	BrandID          uuid.UUID `db:"brand_id" json:"-"`
	Day              time.Time `db:"day" json:"day"`
	SpamRatio        *float64  `db:"spam_ratio" json:"spam_ratio"`
	DomainReputation string    `db:"domain_reputation" json:"domain_reputation"`
	FetchedAt        time.Time `db:"fetched_at" json:"fetched_at"`
}

func (s *Store) SavePostmasterStat(ctx context.Context, p PostmasterStat) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO postmaster_stats (brand_id, day, spam_ratio, domain_reputation, fetched_at) VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (brand_id, day) DO UPDATE SET spam_ratio = EXCLUDED.spam_ratio, domain_reputation = EXCLUDED.domain_reputation, fetched_at = now()`,
		p.BrandID, p.Day, p.SpamRatio, p.DomainReputation)
	return err
}

// LatestPostmaster: the most recent day with data for the brand.
func (s *Store) LatestPostmaster(ctx context.Context, brandID uuid.UUID) (PostmasterStat, error) {
	rows, err := s.db.Query(ctx, `SELECT brand_id, day, spam_ratio, domain_reputation, fetched_at FROM postmaster_stats
		WHERE brand_id = $1 ORDER BY day DESC LIMIT 1`, brandID)
	return one[PostmasterStat](rows, err)
}

// --- list-quality inputs ---

// RecentImportInvalidRate: average invalid + no-mailbox share of the brand's
// imports checked in the last 90 days (nil = no imports).
func (s *Store) RecentImportInvalidRate(ctx context.Context, brandID uuid.UUID) (*float64, error) {
	var r *float64
	err := s.db.QueryRow(ctx, `
		SELECT avg((check_result->'gate'->>'invalid_rate')::float8) FROM imports
		WHERE brand_id = $1 AND check_result IS NOT NULL AND created_at > now() - interval '90 days'`, brandID).Scan(&r)
	return r, err
}

// UnsubscribesSince counts unsubscribe consent records for the brand.
func (s *Store) UnsubscribesSince(ctx context.Context, brandID uuid.UUID, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(DISTINCT contact_id) FROM consent_records
		WHERE brand_id = $1 AND kind = 'unsubscribed' AND occurred_at > $2`, brandID, since).Scan(&n)
	return n, err
}

// UserByEmail looks a user up by email (case-insensitive).
func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	rows, err := s.db.Query(ctx, `SELECT `+userCols+` FROM users WHERE email = $1`, email)
	return one[User](rows, err)
}

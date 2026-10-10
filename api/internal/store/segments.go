package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Segment struct {
	ID        uuid.UUID       `db:"id" json:"id"`
	BrandID   uuid.UUID       `db:"brand_id" json:"-"`
	Name      string          `db:"name" json:"name"`
	Rules     json.RawMessage `db:"rules" json:"rules"`
	CreatedBy *uuid.UUID      `db:"created_by" json:"created_by"`
	CreatedAt time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt time.Time       `db:"updated_at" json:"updated_at"`
}

const segmentCols = `id, brand_id, name, rules, created_by, created_at, updated_at`

func (s *Store) CreateSegment(ctx context.Context, brandID uuid.UUID, name string, rules json.RawMessage, by uuid.UUID) (Segment, error) {
	rows, err := s.db.Query(ctx, `INSERT INTO segments (brand_id, name, rules, created_by) VALUES ($1, $2, $3, $4)
		RETURNING `+segmentCols, brandID, name, rules, by)
	return one[Segment](rows, err)
}

func (s *Store) UpdateSegment(ctx context.Context, brandID, id uuid.UUID, name string, rules json.RawMessage) (Segment, error) {
	rows, err := s.db.Query(ctx, `UPDATE segments SET name = $3, rules = $4, updated_at = now()
		WHERE brand_id = $1 AND id = $2 RETURNING `+segmentCols, brandID, id, name, rules)
	return one[Segment](rows, err)
}

func (s *Store) DeleteSegment(ctx context.Context, brandID, id uuid.UUID) error {
	return expectOne(s.db.Exec(ctx, `DELETE FROM segments WHERE brand_id = $1 AND id = $2`, brandID, id))
}

func (s *Store) SegmentByID(ctx context.Context, brandID, id uuid.UUID) (Segment, error) {
	rows, err := s.db.Query(ctx, `SELECT `+segmentCols+` FROM segments WHERE brand_id = $1 AND id = $2`, brandID, id)
	return one[Segment](rows, err)
}

func (s *Store) ListSegments(ctx context.Context, brandID uuid.UUID) ([]Segment, error) {
	rows, err := s.db.Query(ctx, `SELECT `+segmentCols+` FROM segments WHERE brand_id = $1 ORDER BY name`, brandID)
	return all[Segment](rows, err)
}

// SegmentCounts: how many of the brand's contacts match where (a compiled
// segment over c and s, parameters from $2), and how many of those could be
// emailed today: subscribed, not suppressed, not sunset. Tiers counts the
// mailable ones.
type SegmentCounts struct {
	Matching int          `json:"matching"`
	Mailable int          `json:"mailable"`
	Tiers    map[Tier]int `json:"tiers"`
}

func (s *Store) CountSegment(ctx context.Context, brandID uuid.UUID, where string, args []any) (SegmentCounts, error) {
	out := SegmentCounts{Tiers: map[Tier]int{TierActive: 0, TierWarm: 0, TierAtRisk: 0, TierSunset: 0}}
	rows, err := s.db.Query(ctx, `
		SELECT `+tierExpr+` AS tier, (c.status = 'subscribed' AND s.email_hash IS NULL) AS ok, count(*)
		`+contactFrom+` WHERE c.brand_id = $1 AND `+where+` GROUP BY 1, 2`, append([]any{brandID}, args...)...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var tier Tier
		var ok bool
		var n int
		if err := rows.Scan(&tier, &ok, &n); err != nil {
			return out, err
		}
		out.Matching += n
		if ok {
			out.Tiers[tier] += n
			if tier != TierSunset {
				out.Mailable += n
			}
		}
	}
	return out, rows.Err()
}

// SegmentSample: the first contacts (by email) a segment matches.
func (s *Store) SegmentSample(ctx context.Context, brandID uuid.UUID, where string, args []any, limit int) ([]Contact, error) {
	args = append([]any{brandID}, args...)
	rows, err := s.db.Query(ctx, `SELECT `+contactCols+contactFrom+` WHERE c.brand_id = $1 AND `+where+
		fmt.Sprintf(` ORDER BY c.email LIMIT %d`, limit), args...)
	return all[Contact](rows, err)
}

package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// SendHistory is one contact's recent marketing email, across all brands
// (matched by address).
type SendHistory struct {
	Last24h       int
	Last7d        int
	LastSameBrand *time.Time // last email from the asking brand
}

// SendHistory returns the history of each of the brand's contacts; ids that
// aren't the brand's contacts are missing from the map.
func (s *Store) SendHistory(ctx context.Context, brandID uuid.UUID, contactIDs []uuid.UUID, now time.Time) (map[uuid.UUID]SendHistory, error) {
	rows, err := s.db.Query(ctx, `
		SELECT c.id,
			count(m.id) FILTER (WHERE m.sent_at > $3::timestamptz - interval '24 hours'),
			count(m.id),
			max(m.sent_at) FILTER (WHERE m.brand_id = $1)
		FROM contacts c
		LEFT JOIN marketing_sends m ON m.email_hash = c.email_hash
			AND m.sent_at > $3::timestamptz - interval '7 days' -- no upper bound: a send stamped a moment
			                                                   -- "later" (another worker's clock) still counts
		WHERE c.brand_id = $1 AND c.id = ANY($2)
		GROUP BY c.id`, brandID, contactIDs, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[uuid.UUID]SendHistory, len(contactIDs))
	for rows.Next() {
		var id uuid.UUID
		var h SendHistory
		if err := rows.Scan(&id, &h.Last24h, &h.Last7d, &h.LastSameBrand); err != nil {
			return nil, err
		}
		out[id] = h
	}
	return out, rows.Err()
}

// LockSendHistory takes a transaction-long lock on each contact's address,
// in a fixed order so two batches can't deadlock.
func (s *Store) LockSendHistory(ctx context.Context, brandID uuid.UUID, contactIDs []uuid.UUID) error {
	_, err := s.db.Exec(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended('freqcap:' || encode(h, 'hex'), 0))
		FROM (SELECT DISTINCT email_hash AS h FROM contacts WHERE brand_id = $1 AND id = ANY($2) ORDER BY 1) x`,
		brandID, contactIDs)
	return err
}

// RecordMarketingSends notes one marketing email per contact at `at`.
func (s *Store) RecordMarketingSends(ctx context.Context, brandID uuid.UUID, contactIDs []uuid.UUID, at time.Time) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO marketing_sends (email_hash, brand_id, contact_id, sent_at)
		SELECT email_hash, brand_id, id, $3 FROM contacts WHERE brand_id = $1 AND id = ANY($2)`,
		brandID, contactIDs, at)
	return err
}

func (s *Store) PurgeMarketingSends(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM marketing_sends WHERE sent_at < $1`, before)
	return tag.RowsAffected(), err
}

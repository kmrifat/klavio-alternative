package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// --- double opt-in and re-permission (M7) ---

func (s *Store) SetConfirmationSent(ctx context.Context, contactID uuid.UUID, at time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE contacts SET confirmation_sent_at = $2 WHERE id = $1`, contactID, at)
	return err
}

// ConfirmContact subscribes someone who clicked a confirmation link, if
// their status is one of from (ErrNotFound otherwise). Confirming is fresh
// engagement and makes the consent double opt-in.
func (s *Store) ConfirmContact(ctx context.Context, contactID uuid.UUID, from []ContactStatus, at time.Time) error {
	return expectOne(s.db.Exec(ctx, `
		UPDATE contacts SET status = 'subscribed', consent_basis = 'double_opt_in', consented_at = $3,
			last_engaged_at = GREATEST(last_engaged_at, $3), status_before_unsubscribe = NULL, updated_at = now()
		WHERE id = $1 AND status = ANY($2)`, contactID, from, at))
}

func (s *Store) SetRepermission(ctx context.Context, brandID uuid.UUID, on bool) error {
	return expectOne(s.db.Exec(ctx, `UPDATE brands SET repermission_on = $2, updated_at = now() WHERE id = $1`, brandID, on))
}

// RepermissionSentSince counts re-permission emails the brand sent since t.
func (s *Store) RepermissionSentSince(ctx context.Context, brandID uuid.UUID, t time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM contacts WHERE brand_id = $1 AND repermission_sent_at >= $2`, brandID, t).Scan(&n)
	return n, err
}

// ClaimRepermission picks up to limit quarantined, unsuppressed contacts who
// haven't been asked yet, oldest first, and marks them asked at `at`. Rows
// another worker is claiming are skipped.
func (s *Store) ClaimRepermission(ctx context.Context, brandID uuid.UUID, limit int, at time.Time) ([]Contact, error) {
	rows, err := s.db.Query(ctx, `
		WITH picked AS (
			SELECT c.id FROM contacts c LEFT JOIN suppressions s ON s.email_hash = c.email_hash
			WHERE c.brand_id = $1 AND c.status = 'quarantined' AND c.repermission_sent_at IS NULL AND s.email_hash IS NULL
			ORDER BY c.created_at, c.id LIMIT $2 FOR UPDATE OF c SKIP LOCKED)
		UPDATE contacts SET repermission_sent_at = $3 FROM picked WHERE contacts.id = picked.id RETURNING contacts.id`,
		brandID, limit, at)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return nil, err
	}
	rows, err = s.db.Query(ctx, `SELECT `+contactCols+contactFrom+` WHERE c.brand_id = $1 AND c.id = ANY($2) ORDER BY c.created_at, c.id`, brandID, ids)
	return all[Contact](rows, err)
}

// UnclaimRepermission undoes a claim whose email couldn't be sent.
func (s *Store) UnclaimRepermission(ctx context.Context, contactIDs []uuid.UUID) error {
	_, err := s.db.Exec(ctx, `UPDATE contacts SET repermission_sent_at = NULL WHERE id = ANY($1)`, contactIDs)
	return err
}

// --- topics waiting for a subscriber's confirmation ---

// RequestTopics records topics a subscriber asked for, to add once they confirm.
func (s *Store) RequestTopics(ctx context.Context, brandID, contactID uuid.UUID, topicIDs []uuid.UUID) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO contact_topic_requests (brand_id, contact_id, topic_id)
		SELECT $1, $2, unnest($3::uuid[])
		ON CONFLICT (contact_id, topic_id) DO UPDATE SET requested_at = now()`, brandID, contactID, topicIDs)
	return err
}

func (s *Store) PendingTopicRequests(ctx context.Context, contactID uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM contact_topic_requests WHERE contact_id = $1`, contactID).Scan(&n)
	return n, err
}

// ApplyTopicRequests subscribes the contact to the topics they asked for and
// clears the requests; returns how many topics were new.
func (s *Store) ApplyTopicRequests(ctx context.Context, contactID uuid.UUID) (int, error) {
	tag, err := s.db.Exec(ctx, `
		WITH req AS (DELETE FROM contact_topic_requests WHERE contact_id = $1 RETURNING brand_id, contact_id, topic_id)
		INSERT INTO contact_topics (brand_id, contact_id, topic_id) SELECT brand_id, contact_id, topic_id FROM req
		ON CONFLICT DO NOTHING`, contactID)
	return int(tag.RowsAffected()), err
}

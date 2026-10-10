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

type TopicRequest struct {
	ID          uuid.UUID   `db:"id"`
	BrandID     uuid.UUID   `db:"brand_id"`
	ContactID   uuid.UUID   `db:"contact_id"`
	TopicIDs    []uuid.UUID `db:"topic_ids"`
	RequestedAt time.Time   `db:"requested_at"`
	AppliedAt   *time.Time  `db:"applied_at"`
}

// RequestTopics records topics a subscriber asked for; they're added when
// this request's own confirmation link is clicked.
func (s *Store) RequestTopics(ctx context.Context, brandID, contactID uuid.UUID, topicIDs []uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx, `INSERT INTO contact_topic_requests (brand_id, contact_id, topic_ids) VALUES ($1, $2, $3) RETURNING id`,
		brandID, contactID, topicIDs).Scan(&id)
	return id, err
}

func (s *Store) TopicRequestByID(ctx context.Context, brandID, contactID, id uuid.UUID) (TopicRequest, error) {
	rows, err := s.db.Query(ctx, `SELECT id, brand_id, contact_id, topic_ids, requested_at, applied_at FROM contact_topic_requests
		WHERE brand_id = $1 AND contact_id = $2 AND id = $3`, brandID, contactID, id)
	return one[TopicRequest](rows, err)
}

// ApplyTopicRequest subscribes the contact to that request's topics (those
// that still exist) and marks it applied; returns how many were new.
func (s *Store) ApplyTopicRequest(ctx context.Context, r TopicRequest, at time.Time) (int, error) {
	tag, err := s.db.Exec(ctx, `
		INSERT INTO contact_topics (brand_id, contact_id, topic_id)
		SELECT t.brand_id, $2, t.id FROM topics t WHERE t.brand_id = $1 AND t.id = ANY($3)
		ON CONFLICT DO NOTHING`, r.BrandID, r.ContactID, r.TopicIDs)
	if err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(ctx, `UPDATE contact_topic_requests SET applied_at = $2 WHERE id = $1`, r.ID, at); err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// TopicNames returns the names of the brand's topics among ids, by name.
func (s *Store) TopicNames(ctx context.Context, brandID uuid.UUID, ids []uuid.UUID) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT name FROM topics WHERE brand_id = $1 AND id = ANY($2) ORDER BY name`, brandID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

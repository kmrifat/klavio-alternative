package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Session struct {
	TokenHash   []byte     `db:"token_hash"`
	UserID      uuid.UUID  `db:"user_id"`
	CreatedAt   time.Time  `db:"created_at"`
	ExpiresAt   time.Time  `db:"expires_at"`
	LastSeenAt  time.Time  `db:"last_seen_at"`
	SteppedUpAt *time.Time `db:"stepped_up_at"`
	IP          string     `db:"ip"`
	UserAgent   string     `db:"user_agent"`
}

func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO sessions (token_hash, user_id, expires_at, stepped_up_at, ip, user_agent)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		sess.TokenHash, sess.UserID, sess.ExpiresAt, sess.SteppedUpAt, sess.IP, sess.UserAgent)
	return err
}

// ActiveSession returns an unexpired session.
func (s *Store) ActiveSession(ctx context.Context, tokenHash []byte) (Session, error) {
	rows, err := s.db.Query(ctx, `
		SELECT token_hash, user_id, created_at, expires_at, last_seen_at, stepped_up_at, ip, user_agent
		FROM sessions WHERE token_hash = $1 AND expires_at > now()`, tokenHash)
	return one[Session](rows, err)
}

func (s *Store) TouchSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.db.Exec(ctx, `UPDATE sessions SET last_seen_at = now() WHERE token_hash = $1`, tokenHash)
	return err
}

func (s *Store) MarkSteppedUp(ctx context.Context, tokenHash []byte, at time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE sessions SET stepped_up_at = $2 WHERE token_hash = $1`, tokenHash, at)
	return err
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	return tag.RowsAffected(), err
}

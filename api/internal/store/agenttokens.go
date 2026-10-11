package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// AgentToken lets an AI agent act as its user through MCP (see package mcp).
type AgentToken struct {
	ID         uuid.UUID  `db:"id" json:"id"`
	UserID     uuid.UUID  `db:"user_id" json:"-"`
	Name       string     `db:"name" json:"name"`
	Prefix     string     `db:"prefix" json:"prefix"`
	TokenHash  []byte     `db:"token_hash" json:"-"`
	CreatedAt  time.Time  `db:"created_at" json:"created_at"`
	ExpiresAt  time.Time  `db:"expires_at" json:"expires_at"`
	RevokedAt  *time.Time `db:"revoked_at" json:"revoked_at"`
	LastUsedAt *time.Time `db:"last_used_at" json:"last_used_at"`
}

// Label is how the audit log names the token: "Claude Code (kag_3f2a9c1d)".
func (t AgentToken) Label() string { return t.Name + " (" + t.Prefix + ")" }

// Usable: not revoked and not expired.
func (t AgentToken) Usable(now time.Time) bool { return t.RevokedAt == nil && now.Before(t.ExpiresAt) }

const agentTokenCols = `id, user_id, name, prefix, token_hash, created_at, expires_at, revoked_at, last_used_at`

func (s *Store) CreateAgentToken(ctx context.Context, t AgentToken) (AgentToken, error) {
	// Capped by the database's clock: a full 90 days from the app's clock
	// can land a few milliseconds past the table's limit.
	return one[AgentToken](s.db.Query(ctx, `INSERT INTO agent_tokens (user_id, name, prefix, token_hash, expires_at)
		VALUES ($1, $2, $3, $4, LEAST($5::timestamptz, now() + interval '90 days')) RETURNING `+agentTokenCols,
		t.UserID, t.Name, t.Prefix, t.TokenHash, t.ExpiresAt))
}

func (s *Store) AgentTokenByHash(ctx context.Context, hash []byte) (AgentToken, error) {
	return one[AgentToken](s.db.Query(ctx, `SELECT `+agentTokenCols+` FROM agent_tokens WHERE token_hash = $1`, hash))
}

// AgentTokensForUser: newest first.
func (s *Store) AgentTokensForUser(ctx context.Context, userID uuid.UUID) ([]AgentToken, error) {
	return all[AgentToken](s.db.Query(ctx, `SELECT `+agentTokenCols+` FROM agent_tokens WHERE user_id = $1
		ORDER BY created_at DESC`, userID))
}

// RevokeAgentToken revokes one of the user's tokens; ErrNotFound if it isn't theirs.
func (s *Store) RevokeAgentToken(ctx context.Context, userID, id uuid.UUID) (AgentToken, error) {
	return one[AgentToken](s.db.Query(ctx, `UPDATE agent_tokens SET revoked_at = COALESCE(revoked_at, now())
		WHERE id = $1 AND user_id = $2 RETURNING `+agentTokenCols, id, userID))
}

// TouchAgentToken records a use, at most once a minute.
func (s *Store) TouchAgentToken(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.Exec(ctx, `UPDATE agent_tokens SET last_used_at = now()
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')`, id)
	return err
}

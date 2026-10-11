package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// OAuthClient is an AI client that registered itself for MCP sign-in.
type OAuthClient struct {
	ID           string    `db:"id" json:"client_id"`
	Name         string    `db:"name" json:"client_name"`
	RedirectURIs []string  `db:"redirect_uris" json:"redirect_uris"`
	CreatedAt    time.Time `db:"created_at" json:"-"`
}

func (s *Store) CreateOAuthClient(ctx context.Context, c OAuthClient) (OAuthClient, error) {
	return one[OAuthClient](s.db.Query(ctx, `INSERT INTO oauth_clients (id, name, redirect_uris) VALUES ($1, $2, $3)
		RETURNING id, name, redirect_uris, created_at`, c.ID, c.Name, c.RedirectURIs))
}

func (s *Store) OAuthClientByID(ctx context.Context, id string) (OAuthClient, error) {
	return one[OAuthClient](s.db.Query(ctx, `SELECT id, name, redirect_uris, created_at FROM oauth_clients WHERE id = $1`, id))
}

// OAuthGrant is one sign-in, from the client's request to its token.
type OAuthGrant struct {
	ID            uuid.UUID  `db:"id"`
	ClientID      string     `db:"client_id"`
	ClientName    string     `db:"client_name"`
	RedirectURI   string     `db:"redirect_uri"`
	State         string     `db:"state"`
	CodeChallenge string     `db:"code_challenge"`
	CreatedAt     time.Time  `db:"created_at"`
	ExpiresAt     time.Time  `db:"expires_at"`
	UserID        *uuid.UUID `db:"user_id"`
	ApprovedAt    *time.Time `db:"approved_at"`
	DeniedAt      *time.Time `db:"denied_at"`
	CodeExpiresAt *time.Time `db:"code_expires_at"`
	ExchangedAt   *time.Time `db:"exchanged_at"`
}

// Pending: not decided yet, and still in time.
func (g OAuthGrant) Pending(now time.Time) bool {
	return g.ApprovedAt == nil && g.DeniedAt == nil && now.Before(g.ExpiresAt)
}

const oauthGrantCols = `g.id, g.client_id, c.name AS client_name, g.redirect_uri, g.state, g.code_challenge, g.created_at,
	g.expires_at, g.user_id, g.approved_at, g.denied_at, g.code_expires_at, g.exchanged_at`

func (s *Store) CreateOAuthGrant(ctx context.Context, g OAuthGrant) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx, `INSERT INTO oauth_grants (client_id, redirect_uri, state, code_challenge, expires_at)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`, g.ClientID, g.RedirectURI, g.State, g.CodeChallenge, g.ExpiresAt).Scan(&id)
	return id, err
}

func (s *Store) OAuthGrantByID(ctx context.Context, id uuid.UUID) (OAuthGrant, error) {
	return one[OAuthGrant](s.db.Query(ctx, `SELECT `+oauthGrantCols+` FROM oauth_grants g JOIN oauth_clients c ON c.id = g.client_id
		WHERE g.id = $1`, id))
}

// LockOAuthGrant locks a grant for deciding it.
func (s *Store) LockOAuthGrant(ctx context.Context, id uuid.UUID) (OAuthGrant, error) {
	return one[OAuthGrant](s.db.Query(ctx, `SELECT `+oauthGrantCols+` FROM oauth_grants g JOIN oauth_clients c ON c.id = g.client_id
		WHERE g.id = $1 FOR UPDATE OF g`, id))
}

// LockOAuthGrantByCode locks the grant a code was issued for.
func (s *Store) LockOAuthGrantByCode(ctx context.Context, codeHash []byte) (OAuthGrant, error) {
	return one[OAuthGrant](s.db.Query(ctx, `SELECT `+oauthGrantCols+` FROM oauth_grants g JOIN oauth_clients c ON c.id = g.client_id
		WHERE g.code_hash = $1 FOR UPDATE OF g`, codeHash))
}

func (s *Store) ApproveOAuthGrant(ctx context.Context, id, userID uuid.UUID, codeHash []byte, codeExpiresAt time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE oauth_grants SET user_id = $2, approved_at = now(), code_hash = $3, code_expires_at = $4
		WHERE id = $1`, id, userID, codeHash, codeExpiresAt)
	return err
}

func (s *Store) DenyOAuthGrant(ctx context.Context, id, userID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `UPDATE oauth_grants SET user_id = $2, denied_at = now() WHERE id = $1`, id, userID)
	return err
}

// ExchangeOAuthGrant spends the code: it can't be used again.
func (s *Store) ExchangeOAuthGrant(ctx context.Context, id, tokenID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `UPDATE oauth_grants SET exchanged_at = now(), code_hash = NULL, agent_token_id = $2 WHERE id = $1`, id, tokenID)
	return err
}

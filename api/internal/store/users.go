package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
)

type CompanyRole string

const (
	CompanyMember   CompanyRole = "member"
	CompanyApprover CompanyRole = "approver"
	CompanyOwner    CompanyRole = "owner"
)

func (r CompanyRole) Valid() bool {
	return r == CompanyMember || r == CompanyApprover || r == CompanyOwner
}

type User struct {
	ID           uuid.UUID   `db:"id"`
	Email        string      `db:"email"`
	Name         string      `db:"name"`
	CompanyRole  CompanyRole `db:"company_role"`
	WebAuthnID   []byte      `db:"webauthn_id"`
	CreatedAt    time.Time   `db:"created_at"`
	LastActiveAt *time.Time  `db:"last_active_at"`
	FrozenAt     *time.Time  `db:"frozen_at"`
	FrozenReason string      `db:"frozen_reason"`
}

const userCols = `id, email, name, company_role, webauthn_id, created_at, last_active_at, frozen_at, frozen_reason`

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) CreateUser(ctx context.Context, email, name string, role CompanyRole, webauthnID []byte) (User, error) {
	rows, err := s.db.Query(ctx, `
		INSERT INTO users (email, name, company_role, webauthn_id) VALUES ($1, $2, $3, $4)
		RETURNING `+userCols, email, name, role, webauthnID)
	return one[User](rows, err)
}

func (s *Store) UserByID(ctx context.Context, id uuid.UUID) (User, error) {
	rows, err := s.db.Query(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id)
	return one[User](rows, err)
}

func (s *Store) UserByWebAuthnID(ctx context.Context, webauthnID []byte) (User, error) {
	rows, err := s.db.Query(ctx, `SELECT `+userCols+` FROM users WHERE webauthn_id = $1`, webauthnID)
	return one[User](rows, err)
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY name, email`)
	return all[User](rows, err)
}

func (s *Store) SetCompanyRole(ctx context.Context, id uuid.UUID, role CompanyRole) error {
	tag, err := s.db.Exec(ctx, `UPDATE users SET company_role = $2 WHERE id = $1`, id, role)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) CountOwners(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM users WHERE company_role = 'owner'`).Scan(&n)
	return n, err
}

// LockUsers serialises role changes so two requests can't remove the last owner at once.
func (s *Store) LockUsers(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE`)
	return err
}

func (s *Store) TouchUser(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET last_active_at = now() WHERE id = $1`, id)
	return err
}

// Passkeys

type Passkey struct {
	ID           uuid.UUID           `db:"id"`
	UserID       uuid.UUID           `db:"user_id"`
	CredentialID []byte              `db:"credential_id"`
	Credential   webauthn.Credential `db:"credential"`
	CreatedAt    time.Time           `db:"created_at"`
	LastUsedAt   *time.Time          `db:"last_used_at"`
}

func (s *Store) AddPasskey(ctx context.Context, userID uuid.UUID, cred webauthn.Credential) error {
	raw, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO passkeys (user_id, credential_id, credential) VALUES ($1, $2, $3)`,
		userID, cred.ID, raw)
	return translate(err)
}

func (s *Store) PasskeysForUser(ctx context.Context, userID uuid.UUID) ([]Passkey, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, user_id, credential_id, credential, created_at, last_used_at
		FROM passkeys WHERE user_id = $1 ORDER BY created_at`, userID)
	return all[Passkey](rows, err)
}

// UpdatePasskeyAfterLogin stores the new sign count and flags the library returns.
func (s *Store) UpdatePasskeyAfterLogin(ctx context.Context, cred webauthn.Credential) error {
	raw, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE passkeys SET credential = $2, last_used_at = now() WHERE credential_id = $1`,
		cred.ID, raw)
	return err
}

func (s *Store) CountPasskeys(ctx context.Context) (map[uuid.UUID]int, error) {
	rows, err := s.db.Query(ctx, `SELECT user_id, count(*) FROM passkeys GROUP BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]int{}
	for rows.Next() {
		var id uuid.UUID
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

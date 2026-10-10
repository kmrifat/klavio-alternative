package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type BrandRole string

const (
	RoleViewer     BrandRole = "viewer"
	RoleEditor     BrandRole = "editor"
	RoleSender     BrandRole = "sender"
	RoleBrandAdmin BrandRole = "brand_admin"
)

func (r BrandRole) Valid() bool {
	switch r {
	case RoleViewer, RoleEditor, RoleSender, RoleBrandAdmin:
		return true
	}
	return false
}

// Rank orders roles so "at least editor" checks are simple. Unknown = 0.
func (r BrandRole) Rank() int {
	switch r {
	case RoleViewer:
		return 1
	case RoleEditor:
		return 2
	case RoleSender:
		return 3
	case RoleBrandAdmin:
		return 4
	}
	return 0
}

// BrandRoles maps brand ID → role.
type BrandRoles map[uuid.UUID]BrandRole

func (s *Store) MembershipsForUser(ctx context.Context, userID uuid.UUID) (BrandRoles, error) {
	rows, err := s.db.Query(ctx, `SELECT brand_id, role FROM memberships WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := BrandRoles{}
	for rows.Next() {
		var id uuid.UUID
		var role BrandRole
		if err := rows.Scan(&id, &role); err != nil {
			return nil, err
		}
		out[id] = role
	}
	return out, rows.Err()
}

// AllMemberships returns user ID → brand roles.
func (s *Store) AllMemberships(ctx context.Context) (map[uuid.UUID]BrandRoles, error) {
	rows, err := s.db.Query(ctx, `SELECT user_id, brand_id, role FROM memberships`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]BrandRoles{}
	for rows.Next() {
		var userID, brandID uuid.UUID
		var role BrandRole
		if err := rows.Scan(&userID, &brandID, &role); err != nil {
			return nil, err
		}
		if out[userID] == nil {
			out[userID] = BrandRoles{}
		}
		out[userID][brandID] = role
	}
	return out, rows.Err()
}

// SetMembership sets a role, or removes the membership when role is "".
func (s *Store) SetMembership(ctx context.Context, userID, brandID uuid.UUID, role BrandRole) error {
	if role == "" {
		_, err := s.db.Exec(ctx, `DELETE FROM memberships WHERE user_id = $1 AND brand_id = $2`, userID, brandID)
		return err
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO memberships (user_id, brand_id, role) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, brand_id) DO UPDATE SET role = EXCLUDED.role`, userID, brandID, role)
	return translate(err)
}

// Invites

type Invite struct {
	ID          uuid.UUID   `db:"id"`
	TokenHash   []byte      `db:"token_hash"`
	Email       string      `db:"email"`
	Name        string      `db:"name"`
	CompanyRole CompanyRole `db:"company_role"`
	BrandRoles  BrandRoles  `db:"brand_roles"`
	WebAuthnID  []byte      `db:"webauthn_id"`
	InvitedBy   *uuid.UUID  `db:"invited_by"`
	CreatedAt   time.Time   `db:"created_at"`
	ExpiresAt   time.Time   `db:"expires_at"`
	AcceptedAt  *time.Time  `db:"accepted_at"`
}

const inviteCols = `id, token_hash, email, name, company_role, brand_roles, webauthn_id, invited_by, created_at, expires_at, accepted_at`

func (s *Store) CreateInvite(ctx context.Context, inv Invite) (Invite, error) {
	roles, err := json.Marshal(inv.BrandRoles)
	if err != nil {
		return Invite{}, err
	}
	rows, err := s.db.Query(ctx, `
		INSERT INTO invites (token_hash, email, name, company_role, brand_roles, webauthn_id, invited_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+inviteCols,
		inv.TokenHash, inv.Email, inv.Name, inv.CompanyRole, roles, inv.WebAuthnID, inv.InvitedBy, inv.ExpiresAt)
	return one[Invite](rows, err)
}

// OpenInvite returns an unaccepted, unexpired invite.
func (s *Store) OpenInvite(ctx context.Context, tokenHash []byte) (Invite, error) {
	rows, err := s.db.Query(ctx, `SELECT `+inviteCols+` FROM invites
		WHERE token_hash = $1 AND accepted_at IS NULL AND expires_at > now()`, tokenHash)
	return one[Invite](rows, err)
}

// AcceptInvite marks it used; returns ErrNotFound if someone else got there first.
func (s *Store) AcceptInvite(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `UPDATE invites SET accepted_at = now() WHERE id = $1 AND accepted_at IS NULL`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// DeleteExpiredOpenInvite frees the email for a new invite.
func (s *Store) DeleteExpiredOpenInvite(ctx context.Context, email string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM invites WHERE email = $1 AND accepted_at IS NULL AND expires_at <= now()`, email)
	return err
}

// DeleteOpenInvites cancels every unaccepted invite.
func (s *Store) DeleteOpenInvites(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `DELETE FROM invites WHERE accepted_at IS NULL`)
	return err
}

func (s *Store) PendingInvites(ctx context.Context) ([]Invite, error) {
	rows, err := s.db.Query(ctx, `SELECT `+inviteCols+` FROM invites
		WHERE accepted_at IS NULL AND expires_at > now() ORDER BY created_at DESC`)
	return all[Invite](rows, err)
}

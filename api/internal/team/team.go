// Package team serves Team & access (S9): members, roles per brand, invites
// and the recent audit log.
package team

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

type Handlers struct {
	Store *store.Store
	Auth  *auth.Service
	Log   *slog.Logger
}

func (t *Handlers) Routes(mux *http.ServeMux) {
	h := func(f httpx.HandlerFunc) http.HandlerFunc { return httpx.Handle(t.Log, f) }
	mux.HandleFunc("GET /admin/v1/team", h(t.list))
	mux.HandleFunc("POST /admin/v1/team/invites", h(t.invite))
	mux.HandleFunc("PUT /admin/v1/team/members/{id}/roles", h(t.setRoles))
	mux.HandleFunc("GET /admin/v1/audit", h(t.audit))
}

type member struct {
	ID           uuid.UUID         `json:"id"`
	Email        string            `json:"email"`
	Name         string            `json:"name"`
	CompanyRole  store.CompanyRole `json:"company_role"`
	BrandRoles   store.BrandRoles  `json:"brand_roles"`
	Passkeys     int               `json:"passkeys"`
	LastActiveAt *time.Time        `json:"last_active_at"`
}

type pendingInvite struct {
	ID          uuid.UUID         `json:"id"`
	Email       string            `json:"email"`
	Name        string            `json:"name"`
	CompanyRole store.CompanyRole `json:"company_role"`
	BrandRoles  store.BrandRoles  `json:"brand_roles"`
	ExpiresAt   time.Time         `json:"expires_at"`
}

// list: every signed-in member can see who has access to what.
func (t *Handlers) list(w http.ResponseWriter, r *http.Request) error {
	if _, err := auth.Require(r); err != nil {
		return err
	}
	ctx := r.Context()
	users, err := t.Store.ListUsers(ctx)
	if err != nil {
		return err
	}
	roles, err := t.Store.AllMemberships(ctx)
	if err != nil {
		return err
	}
	keys, err := t.Store.CountPasskeys(ctx)
	if err != nil {
		return err
	}
	invites, err := t.Store.PendingInvites(ctx)
	if err != nil {
		return err
	}

	out := struct {
		Members []member        `json:"members"`
		Invites []pendingInvite `json:"invites"`
	}{Members: []member{}, Invites: []pendingInvite{}}
	for _, u := range users {
		br := roles[u.ID]
		if br == nil {
			br = store.BrandRoles{}
		}
		out.Members = append(out.Members, member{
			ID: u.ID, Email: u.Email, Name: u.Name, CompanyRole: u.CompanyRole,
			BrandRoles: br, Passkeys: keys[u.ID], LastActiveAt: u.LastActiveAt,
		})
	}
	for _, i := range invites {
		out.Invites = append(out.Invites, pendingInvite{
			ID: i.ID, Email: i.Email, Name: i.Name, CompanyRole: i.CompanyRole,
			BrandRoles: i.BrandRoles, ExpiresAt: i.ExpiresAt,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

type rolesInput struct {
	CompanyRole store.CompanyRole `json:"company_role"`
	BrandRoles  store.BrandRoles  `json:"brand_roles"`
}

func (t *Handlers) requireOwner(r *http.Request) (*auth.Principal, error) {
	p, err := t.Auth.RequireStepUp(r)
	if err != nil {
		return nil, err
	}
	if !p.IsOwner() {
		return nil, httpx.ErrForbidden
	}
	return p, nil
}

// knownBrands rejects roles for brands that don't exist.
func knownBrands(brands []store.Brand, roles store.BrandRoles) error {
	ids := map[uuid.UUID]bool{}
	for _, b := range brands {
		ids[b.ID] = true
	}
	for id := range roles {
		if !ids[id] {
			return httpx.Errorf(http.StatusBadRequest, "unknown_brand", "brand %s doesn't exist", id)
		}
	}
	return nil
}

func (t *Handlers) invite(w http.ResponseWriter, r *http.Request) error {
	p, err := t.requireOwner(r)
	if err != nil {
		return err
	}
	var in struct {
		Email string `json:"email"`
		Name  string `json:"name"`
		rolesInput
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()

	var link string
	var inv store.Invite
	err = t.Store.InTx(ctx, func(tx *store.Store) error {
		brands, err := tx.ListBrands(ctx)
		if err != nil {
			return err
		}
		if err := knownBrands(brands, in.BrandRoles); err != nil {
			return err
		}
		inv, link, err = t.Auth.CreateInvite(ctx, tx, &p.User.ID, auth.InviteInput{
			Email: in.Email, Name: in.Name, CompanyRole: in.CompanyRole, BrandRoles: in.BrandRoles,
		})
		if err != nil {
			return err
		}
		return tx.Audit(ctx, auth.AuditEntry(r, "user.invited", "invite", inv.Email, nil), nil,
			map[string]any{"company_role": inv.CompanyRole, "brand_roles": inv.BrandRoles})
	})
	if err != nil {
		return err
	}
	// The link is shown once; only its hash is stored.
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"invite_url": link, "expires_at": inv.ExpiresAt})
	return nil
}

func (t *Handlers) setRoles(w http.ResponseWriter, r *http.Request) error {
	p, err := t.requireOwner(r)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return httpx.ErrNotFound
	}
	var in rolesInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	if !in.CompanyRole.Valid() {
		return httpx.Errorf(http.StatusBadRequest, "invalid_role", "unknown company role %q", in.CompanyRole)
	}
	for _, role := range in.BrandRoles {
		if role != "" && !role.Valid() {
			return httpx.Errorf(http.StatusBadRequest, "invalid_role", "unknown brand role %q", role)
		}
	}
	ctx := r.Context()

	err = t.Store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.LockUsers(ctx); err != nil {
			return err
		}
		target, err := tx.UserByID(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		if target.ID == p.User.ID && in.CompanyRole != target.CompanyRole {
			return httpx.Errorf(http.StatusBadRequest, "own_role", "you can't change your own company role; ask another owner")
		}
		if target.CompanyRole == store.CompanyOwner && in.CompanyRole != store.CompanyOwner {
			n, err := tx.CountOwners(ctx)
			if err != nil {
				return err
			}
			if n <= 1 {
				return httpx.Errorf(http.StatusBadRequest, "last_owner", "there must always be at least one owner")
			}
		}
		brands, err := tx.ListBrands(ctx)
		if err != nil {
			return err
		}
		if err := knownBrands(brands, in.BrandRoles); err != nil {
			return err
		}
		before, err := tx.MembershipsForUser(ctx, id)
		if err != nil {
			return err
		}

		if err := tx.SetCompanyRole(ctx, id, in.CompanyRole); err != nil {
			return err
		}
		// Brands left out of the request keep their current role.
		for brandID, role := range in.BrandRoles {
			if err := tx.SetMembership(ctx, id, brandID, role); err != nil {
				return err
			}
		}
		after, err := tx.MembershipsForUser(ctx, id)
		if err != nil {
			return err
		}
		return tx.Audit(ctx, auth.AuditEntry(r, "user.roles_changed", "user", target.Email, nil),
			map[string]any{"company_role": target.CompanyRole, "brand_roles": before},
			map[string]any{"company_role": in.CompanyRole, "brand_roles": after})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (t *Handlers) audit(w http.ResponseWriter, r *http.Request) error {
	p, err := auth.Require(r)
	if err != nil {
		return err
	}
	if !p.SeesAllBrands() {
		return httpx.ErrForbidden
	}
	entries, err := t.Store.RecentAudit(r.Context(), 50)
	if err != nil {
		return err
	}
	if entries == nil {
		entries = []store.AuditEntry{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"entries": entries})
	return nil
}

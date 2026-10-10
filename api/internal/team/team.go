// Package team serves Team & access (S9): members, roles per brand, invites,
// frozen accounts and anomaly alerts, plus the audit log (S16).
package team

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/approvals"
	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

type Handlers struct {
	Store     *store.Store
	Auth      *auth.Service
	Approvals *approvals.Service
	Log       *slog.Logger
}

// RegisterApprovals adds the team's approval kinds. Call once at startup.
func (t *Handlers) RegisterApprovals() {
	t.Approvals.Register(approvals.KindRoleChange, approvals.Definition{
		Label: "Give someone admin rights", CompanyLevel: true, Execute: t.executeRoleChange,
	})
	t.Approvals.Register(approvals.KindAdminInvite, approvals.Definition{
		Label: "Invite someone with admin rights", CompanyLevel: true,
		// Approving makes the already-created invite link usable.
		Execute: func(context.Context, *store.Store, store.Approval) (any, error) { return nil, nil },
		OnReject: func(ctx context.Context, tx *store.Store, a store.Approval) error {
			return tx.DeleteInviteByApproval(ctx, a.ID)
		},
	})
}

func (t *Handlers) Routes(mux *http.ServeMux) {
	h := func(f httpx.HandlerFunc) http.HandlerFunc { return httpx.Handle(t.Log, f) }
	mux.HandleFunc("GET /admin/v1/team", h(t.list))
	mux.HandleFunc("POST /admin/v1/team/invites", h(t.invite))
	mux.HandleFunc("PUT /admin/v1/team/members/{id}/roles", h(t.setRoles))
	mux.HandleFunc("POST /admin/v1/team/members/{id}/unfreeze", h(t.unfreeze))
	mux.HandleFunc("GET /admin/v1/audit", h(t.audit))
	mux.HandleFunc("GET /admin/v1/alerts", h(t.alerts))
	mux.HandleFunc("POST /admin/v1/alerts/{id}/acknowledge", h(t.acknowledge))
}

type member struct {
	ID           uuid.UUID         `json:"id"`
	Email        string            `json:"email"`
	Name         string            `json:"name"`
	CompanyRole  store.CompanyRole `json:"company_role"`
	BrandRoles   store.BrandRoles  `json:"brand_roles"`
	Passkeys     int               `json:"passkeys"`
	LastActiveAt *time.Time        `json:"last_active_at"`
	FrozenAt     *time.Time        `json:"frozen_at"`
	FrozenReason string            `json:"frozen_reason"`
}

type pendingInvite struct {
	ID               uuid.UUID         `json:"id"`
	Email            string            `json:"email"`
	Name             string            `json:"name"`
	CompanyRole      store.CompanyRole `json:"company_role"`
	BrandRoles       store.BrandRoles  `json:"brand_roles"`
	ExpiresAt        time.Time         `json:"expires_at"`
	AwaitingApproval bool              `json:"awaiting_approval"`
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
			FrozenAt: u.FrozenAt, FrozenReason: u.FrozenReason,
		})
	}
	for _, i := range invites {
		st, err := t.Store.InviteApprovalStatus(ctx, i)
		if err != nil {
			return err
		}
		if st != "" && st != store.ApprovalPending && st != store.ApprovalApproved {
			continue // rejected, cancelled or expired: the link is dead
		}
		out.Invites = append(out.Invites, pendingInvite{
			ID: i.ID, Email: i.Email, Name: i.Name, CompanyRole: i.CompanyRole,
			BrandRoles: i.BrandRoles, ExpiresAt: i.ExpiresAt, AwaitingApproval: st == store.ApprovalPending,
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

func companyRank(r store.CompanyRole) int {
	switch r {
	case store.CompanyApprover:
		return 1
	case store.CompanyOwner:
		return 2
	}
	return 0
}

// elevates: does the change grant owner, approver or brand admin rights the
// person didn't have? Those are "admin changes" and need a second admin (S9).
func elevates(fromCompany store.CompanyRole, from store.BrandRoles, toCompany store.CompanyRole, to store.BrandRoles) bool {
	if companyRank(toCompany) > companyRank(fromCompany) {
		return true
	}
	for brandID, role := range to {
		if role == store.RoleBrandAdmin && from[brandID] != store.RoleBrandAdmin {
			return true
		}
	}
	return false
}

type approvalPending struct {
	Approval store.Approval `json:"approval"`
	Message  string         `json:"message"`
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
	input := auth.InviteInput{Email: in.Email, Name: in.Name, CompanyRole: in.CompanyRole, BrandRoles: in.BrandRoles}
	if err := input.Normalize(); err != nil {
		return err
	}

	var link string
	var inv store.Invite
	var pending *store.Approval
	var noSecondAdmin bool
	err = t.Store.InTx(ctx, func(tx *store.Store) error {
		brands, err := tx.ListBrands(ctx)
		if err != nil {
			return err
		}
		if err := knownBrands(brands, input.BrandRoles); err != nil {
			return err
		}
		if elevates(store.CompanyMember, store.BrandRoles{}, input.CompanyRole, input.BrandRoles) {
			a, err := t.Approvals.Request(ctx, tx, approvals.Request{
				Kind: approvals.KindAdminInvite, RequestedBy: p.User,
				Summary: "Invite " + input.Email + " as " + roleSummary(input.CompanyRole, input.BrandRoles, brands),
				Payload: map[string]any{"email": input.Email, "company_role": input.CompanyRole, "brand_roles": input.BrandRoles},
				Audit:   auth.AuditEntry(r, "", "", "", nil),
			})
			switch {
			case errors.Is(err, approvals.ErrNoApprover):
				noSecondAdmin = true // a one-admin company can't get a second opinion yet
			case err != nil:
				return err
			default:
				pending, input.ApprovalID = &a, &a.ID
			}
		}
		inv, link, err = t.Auth.CreateInvite(ctx, tx, &p.User.ID, input)
		if err != nil {
			return err
		}
		return tx.Audit(ctx, auth.AuditEntry(r, "user.invited", "invite", inv.Email, nil), nil,
			map[string]any{"company_role": inv.CompanyRole, "brand_roles": inv.BrandRoles,
				"awaiting_approval": pending != nil, "no_second_admin": noSecondAdmin})
	})
	if err != nil {
		return err
	}
	// The link is shown once; only its hash is stored.
	out := map[string]any{"invite_url": link, "expires_at": inv.ExpiresAt, "awaiting_approval": pending != nil}
	if pending != nil {
		out["approval"] = pending
	}
	httpx.WriteJSON(w, http.StatusCreated, out)
	return nil
}

func roleSummary(company store.CompanyRole, brandRoles store.BrandRoles, brands []store.Brand) string {
	var parts []string
	if company != store.CompanyMember && company != "" {
		parts = append(parts, string(company))
	}
	for _, b := range brands {
		if r := brandRoles[b.ID]; r != "" {
			parts = append(parts, string(r)+" of "+b.Name)
		}
	}
	if len(parts) == 0 {
		return "member"
	}
	return strings.Join(parts, ", ")
}

type roleChange struct {
	UserID      uuid.UUID         `json:"user_id"`
	CompanyRole store.CompanyRole `json:"company_role"`
	BrandRoles  store.BrandRoles  `json:"brand_roles"`
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

	var pending *store.Approval
	err = t.Store.InTx(ctx, func(tx *store.Store) error {
		target, before, brands, err := t.checkRoleChange(ctx, tx, p.User.ID, roleChange{UserID: id, CompanyRole: in.CompanyRole, BrandRoles: in.BrandRoles})
		if err != nil {
			return err
		}
		change := roleChange{UserID: id, CompanyRole: in.CompanyRole, BrandRoles: in.BrandRoles}
		noSecondAdmin := false
		if elevates(target.CompanyRole, before, in.CompanyRole, in.BrandRoles) {
			a, err := t.Approvals.Request(ctx, tx, approvals.Request{
				Kind: approvals.KindRoleChange, RequestedBy: p.User,
				Summary: "Make " + target.Email + " " + roleSummary(in.CompanyRole, mergedRoles(before, in.BrandRoles), brands),
				Payload: change, Audit: auth.AuditEntry(r, "", "", "", nil),
			})
			switch {
			case errors.Is(err, approvals.ErrNoApprover):
				noSecondAdmin = true // a one-admin company can't get a second opinion yet
			case err != nil:
				return err
			default:
				pending = &a
				return nil
			}
		}
		e := auth.AuditEntry(r, "user.roles_changed", "user", target.Email, nil)
		return t.applyRoles(ctx, tx, e, target, before, change, noSecondAdmin)
	})
	if err != nil {
		return err
	}
	if pending != nil {
		httpx.WriteJSON(w, http.StatusAccepted, approvalPending{Approval: *pending,
			Message: "Giving admin rights needs a second admin. The change is waiting in Approvals."})
		return nil
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func mergedRoles(before, change store.BrandRoles) store.BrandRoles {
	out := store.BrandRoles{}
	for k, v := range before {
		out[k] = v
	}
	for k, v := range change {
		if v == "" {
			delete(out, k)
		} else {
			out[k] = v
		}
	}
	return out
}

// checkRoleChange validates a change against the current state (also run
// again when an approved change executes, since things may have moved on).
func (t *Handlers) checkRoleChange(ctx context.Context, tx *store.Store, actorID uuid.UUID, c roleChange) (store.User, store.BrandRoles, []store.Brand, error) {
	if err := tx.LockUsers(ctx); err != nil {
		return store.User{}, nil, nil, err
	}
	target, err := tx.UserByID(ctx, c.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return target, nil, nil, httpx.ErrNotFound
	}
	if err != nil {
		return target, nil, nil, err
	}
	if target.ID == actorID && c.CompanyRole != target.CompanyRole {
		return target, nil, nil, httpx.Errorf(http.StatusBadRequest, "own_role", "you can't change your own company role; ask another owner")
	}
	if target.CompanyRole == store.CompanyOwner && c.CompanyRole != store.CompanyOwner {
		n, err := tx.CountOwners(ctx)
		if err != nil {
			return target, nil, nil, err
		}
		if n <= 1 {
			return target, nil, nil, httpx.Errorf(http.StatusBadRequest, "last_owner", "there must always be at least one owner")
		}
	}
	brands, err := tx.ListBrands(ctx)
	if err != nil {
		return target, nil, nil, err
	}
	if err := knownBrands(brands, c.BrandRoles); err != nil {
		return target, nil, nil, err
	}
	before, err := tx.MembershipsForUser(ctx, c.UserID)
	return target, before, brands, err
}

func (t *Handlers) applyRoles(ctx context.Context, tx *store.Store, e store.AuditEntry, target store.User, before store.BrandRoles, c roleChange, noSecondAdmin bool) error {
	if err := tx.SetCompanyRole(ctx, c.UserID, c.CompanyRole); err != nil {
		return err
	}
	// Brands left out of the change keep their current role.
	for brandID, role := range c.BrandRoles {
		if err := tx.SetMembership(ctx, c.UserID, brandID, role); err != nil {
			return err
		}
	}
	after, err := tx.MembershipsForUser(ctx, c.UserID)
	if err != nil {
		return err
	}
	return tx.Audit(ctx, e,
		map[string]any{"company_role": target.CompanyRole, "brand_roles": before},
		map[string]any{"company_role": c.CompanyRole, "brand_roles": after, "no_second_admin": noSecondAdmin})
}

func (t *Handlers) executeRoleChange(ctx context.Context, tx *store.Store, a store.Approval) (any, error) {
	var c roleChange
	if err := json.Unmarshal(a.Payload, &c); err != nil {
		return nil, err
	}
	target, before, _, err := t.checkRoleChange(ctx, tx, a.RequestedBy, c)
	if err != nil {
		return nil, err
	}
	requester, err := tx.UserByID(ctx, a.RequestedBy)
	if err != nil {
		return nil, err
	}
	e := store.AuditEntry{ActorID: &requester.ID, ActorEmail: requester.Email, Action: "user.roles_changed",
		TargetType: "user", TargetID: target.Email}
	return nil, t.applyRoles(ctx, tx, e, target, before, c, false)
}

// unfreeze lets an owner restore a frozen account after checking the alert.
func (t *Handlers) unfreeze(w http.ResponseWriter, r *http.Request) error {
	p, err := t.requireOwner(r)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return httpx.ErrNotFound
	}
	ctx := r.Context()
	err = t.Store.InTx(ctx, func(tx *store.Store) error {
		u, err := tx.UserByID(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := tx.Unfreeze(ctx, id); errors.Is(err, store.ErrNotFound) {
			return httpx.Errorf(http.StatusConflict, "not_frozen", "%s isn't frozen", u.Email)
		} else if err != nil {
			return err
		}
		return tx.Audit(ctx, auth.AuditEntry(r, "user.unfrozen", "user", u.Email, nil),
			map[string]any{"frozen_reason": u.FrozenReason}, map[string]any{"by": p.User.Email})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// audit (S16): owners and approvers see everything; brand admins see their
// brands' entries. Filters: brand (slug), actor (email prefix), action
// (prefix), since/until (dates), before (id, for paging).
func (t *Handlers) audit(w http.ResponseWriter, r *http.Request) error {
	p, err := auth.Require(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	q := r.URL.Query()
	f := store.AuditFilter{Actor: strings.TrimSpace(q.Get("actor")), Action: strings.TrimSpace(q.Get("action"))}
	if slug := q.Get("brand"); slug != "" {
		b, err := t.Store.BrandBySlug(ctx, slug)
		if err != nil || !p.CanViewBrand(b.ID) {
			return httpx.ErrNotFound
		}
		f.BrandID = &b.ID
	}
	if !p.SeesAllBrands() {
		if f.BrandID == nil || !p.HasBrandRole(*f.BrandID, store.RoleBrandAdmin) {
			return httpx.ErrForbidden
		}
	}
	for key, dst := range map[string]**time.Time{"since": &f.Since, "until": &f.Until} {
		if v := q.Get(key); v != "" {
			d, err := time.Parse("2006-01-02", v)
			if err != nil {
				return httpx.Errorf(http.StatusBadRequest, "bad_date", "%s must look like 2026-10-01", key)
			}
			if key == "until" {
				d = d.Add(24 * time.Hour) // inclusive of that day
			}
			*dst = &d
		}
	}
	if v, err := strconv.ParseInt(q.Get("before"), 10, 64); err == nil {
		f.Before = v
	}
	f.Limit = 50
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= 200 {
		f.Limit = v
	}
	entries, err := t.Store.QueryAudit(ctx, f)
	if err != nil {
		return err
	}
	if entries == nil {
		entries = []store.AuditEntry{}
	}
	var next int64 // a full page means there may be more
	if len(entries) == f.Limit {
		next = entries[len(entries)-1].ID
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"entries": entries, "next_before": next})
	return nil
}

func (t *Handlers) alerts(w http.ResponseWriter, r *http.Request) error {
	p, err := auth.Require(r)
	if err != nil {
		return err
	}
	if !p.SeesAllBrands() {
		return httpx.ErrForbidden
	}
	list, err := t.Store.ListAlerts(r.Context(), r.URL.Query().Get("open") == "true", 100)
	if err != nil {
		return err
	}
	if list == nil {
		list = []store.Alert{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"alerts": list})
	return nil
}

func (t *Handlers) acknowledge(w http.ResponseWriter, r *http.Request) error {
	p, err := auth.Require(r)
	if err != nil {
		return err
	}
	if !p.SeesAllBrands() {
		return httpx.ErrForbidden
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return httpx.ErrNotFound
	}
	if err := t.Store.AcknowledgeAlert(r.Context(), id, p.User.ID); errors.Is(err, store.ErrNotFound) {
		return httpx.ErrNotFound
	} else if err != nil {
		return err
	}
	if err := t.Store.Audit(r.Context(), auth.AuditEntry(r, "alert.acknowledged", "alert", id.String(), nil), nil, nil); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

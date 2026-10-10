// Package contactsapi is the Contacts API (S14): brands' websites and apps
// add people through it, always with double opt-in. This file is the admin
// side: API keys, consent wordings, the call log and re-permission emails.
package contactsapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/approvals"
	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/brands"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/limits"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// Scopes an API key can have.
const (
	ScopeWrite = "contacts:write" // add people (double opt-in) and unsubscribe them
	ScopeRead  = "contacts:read"  // look up someone's status
)

var allScopes = []string{ScopeWrite, ScopeRead}

// MaxKeyDays: keys expire after at most 90 days.
const MaxKeyDays = 90

const keyPrefix = "kla_"

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// newKey returns the full secret (shown once) and its public prefix.
func newKey() (secret, prefix string, err error) {
	var id [5]byte
	var sec [32]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", "", err
	}
	if _, err := rand.Read(sec[:]); err != nil {
		return "", "", err
	}
	prefix = keyPrefix + strings.ToLower(b32.EncodeToString(id[:]))
	return prefix + "_" + strings.ToLower(b32.EncodeToString(sec[:])), prefix, nil
}

func hashKey(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

type stepUpper interface {
	RequireStepUp(r *http.Request) (*auth.Principal, error)
}

type Admin struct {
	Store     *store.Store
	Auth      stepUpper
	Approvals *approvals.Service
	Limit     func(ctx context.Context, key string) float64
	Log       *slog.Logger
	now       func() time.Time
}

func (a *Admin) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// RegisterApprovals: a new key works once a second admin approves it.
func (a *Admin) RegisterApprovals() {
	a.Approvals.Register(approvals.KindAPIKey, approvals.Definition{
		Label: "Create a Contacts API key",
		Execute: func(ctx context.Context, tx *store.Store, ap store.Approval) (any, error) {
			k, err := tx.APIKeyByApproval(ctx, ap.ID)
			if err != nil {
				return nil, err
			}
			if k.RevokedAt != nil {
				return nil, approvals.Reject("the key was revoked")
			}
			u, err := tx.UserByID(ctx, ap.RequestedBy)
			if err != nil {
				return nil, err
			}
			roles, err := tx.MembershipsForUser(ctx, u.ID)
			if err != nil {
				return nil, err
			}
			if u.FrozenAt != nil || (u.CompanyRole != store.CompanyOwner && roles[k.BrandID] != store.RoleBrandAdmin) {
				return nil, approvals.Reject("%s can no longer create keys for this brand", u.Email)
			}
			return map[string]string{"prefix": k.Prefix}, tx.ActivateAPIKey(ctx, k.ID)
		},
		OnReject: func(ctx context.Context, tx *store.Store, ap store.Approval) error {
			k, err := tx.APIKeyByApproval(ctx, ap.ID)
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			if err != nil || k.RevokedAt != nil {
				return err
			}
			return tx.RevokeAPIKey(ctx, k.BrandID, k.ID, nil)
		},
	})
}

func (a *Admin) Routes(mux *http.ServeMux) {
	f := func(fn httpx.HandlerFunc) http.HandlerFunc { return httpx.Handle(a.Log, fn) }
	mux.HandleFunc("GET /admin/v1/brands/{slug}/api-keys", f(a.listKeys))
	mux.HandleFunc("POST /admin/v1/brands/{slug}/api-keys", f(a.createKey))
	mux.HandleFunc("POST /admin/v1/brands/{slug}/api-keys/{id}/revoke", f(a.revokeKey))
	mux.HandleFunc("GET /admin/v1/brands/{slug}/api-calls", f(a.calls))
	mux.HandleFunc("GET /admin/v1/brands/{slug}/consent-wordings", f(a.listWordings))
	mux.HandleFunc("POST /admin/v1/brands/{slug}/consent-wordings", f(a.createWording))
	mux.HandleFunc("POST /admin/v1/brands/{slug}/consent-wordings/{id}/retire", f(a.retireWording))
	mux.HandleFunc("GET /admin/v1/brands/{slug}/repermission", f(a.repermission))
	mux.HandleFunc("PUT /admin/v1/brands/{slug}/repermission", f(a.setRepermission))
}

func bad(code, format string, args ...any) error {
	return httpx.Errorf(http.StatusBadRequest, code, format, args...)
}

// --- keys ---

type keyView struct {
	store.APIKey
	State    string          `json:"state"` // pending_approval | active | expired | revoked
	Approval *store.Approval `json:"approval,omitempty"`
}

func (a *Admin) view(k store.APIKey) keyView {
	v := keyView{APIKey: k}
	switch {
	case k.RevokedAt != nil:
		v.State = "revoked"
	case !k.ExpiresAt.After(a.clock()):
		v.State = "expired"
	case k.Active:
		v.State = "active"
	default:
		v.State = "pending_approval"
	}
	if v.IPAllowlist == nil {
		v.IPAllowlist = []string{}
	}
	return v
}

func (a *Admin) listKeys(w http.ResponseWriter, r *http.Request) error {
	b, _, err := brands.WithRole(r, a.Store, store.RoleBrandAdmin)
	if err != nil {
		return err
	}
	keys, err := a.Store.ListAPIKeys(r.Context(), b.ID)
	if err != nil {
		return err
	}
	out := []keyView{}
	for _, k := range keys {
		out = append(out, a.view(k))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"keys": out, "scopes": allScopes})
	return nil
}

type keyInput struct {
	Name        string   `json:"name"`
	Scopes      []string `json:"scopes"`
	IPAllowlist []string `json:"ip_allowlist"`
	ExpiresDays int      `json:"expires_in_days"`
	Reason      string   `json:"reason"`
}

func (in *keyInput) normalize() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 80 {
		return bad("invalid_name", "name the key after where it's used, e.g. “Website signup form”")
	}
	if len(in.Scopes) == 0 {
		return bad("invalid_scopes", "pick at least one scope")
	}
	slices.Sort(in.Scopes)
	in.Scopes = slices.Compact(in.Scopes)
	for _, s := range in.Scopes {
		if !slices.Contains(allScopes, s) {
			return bad("invalid_scopes", "unknown scope %q", s)
		}
	}
	if in.ExpiresDays == 0 {
		in.ExpiresDays = MaxKeyDays
	}
	if in.ExpiresDays < 1 || in.ExpiresDays > MaxKeyDays {
		return bad("invalid_expiry", "keys expire after 1 to %d days", MaxKeyDays)
	}
	if len(in.IPAllowlist) > 50 {
		return bad("invalid_ip_allowlist", "at most 50 addresses")
	}
	var cidrs []string
	for _, s := range in.IPAllowlist {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		p, err := parsePrefix(s)
		if err != nil {
			return bad("invalid_ip_allowlist", "%q is not an IP address or range", s)
		}
		if p.Bits() == 0 {
			return bad("invalid_ip_allowlist", "%q would allow every address; leave the list empty for that", s)
		}
		cidrs = append(cidrs, p.String())
	}
	in.IPAllowlist = cidrs
	if in.IPAllowlist == nil {
		in.IPAllowlist = []string{}
	}
	return nil
}

// parsePrefix accepts "203.0.113.7" or "203.0.113.0/24".
func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		return p.Masked(), err
	}
	ip, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(ip, ip.BitLen()), nil
}

// createKey makes the key now (shown once) but inactive until a second
// admin approves; with only one admin in the company it works at once.
func (a *Admin) createKey(w http.ResponseWriter, r *http.Request) error {
	b, _, err := brands.WithRole(r, a.Store, store.RoleBrandAdmin)
	if err != nil {
		return err
	}
	p, err := a.Auth.RequireStepUp(r)
	if err != nil {
		return err
	}
	var in keyInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	if err := in.normalize(); err != nil {
		return err
	}
	secret, prefix, err := newKey()
	if err != nil {
		return err
	}
	ctx := r.Context()
	now := a.clock()
	var k store.APIKey
	var pending *store.Approval
	err = a.Store.InTx(ctx, func(tx *store.Store) error {
		k, err = tx.CreateAPIKey(ctx, store.APIKey{
			BrandID: b.ID, Name: in.Name, Prefix: prefix, KeyHash: hashKey(secret), Scopes: in.Scopes,
			IPAllowlist: in.IPAllowlist, CreatedBy: &p.User.ID, CreatedAt: now, ExpiresAt: now.AddDate(0, 0, in.ExpiresDays),
		})
		if err != nil {
			return err
		}
		ap, err := a.Approvals.Request(ctx, tx, approvals.Request{
			Kind: approvals.KindAPIKey, BrandID: &b.ID, RequestedBy: p.User, Reason: in.Reason,
			Summary: fmt.Sprintf("Contacts API key “%s” for %s (%s, %d days%s)", in.Name, b.Name, strings.Join(in.Scopes, ", "),
				in.ExpiresDays, map[bool]string{true: ", any IP", false: ", IP-restricted"}[len(in.IPAllowlist) == 0]),
			Payload: map[string]any{"key_id": k.ID, "prefix": prefix, "scopes": in.Scopes, "ip_allowlist": in.IPAllowlist},
			Audit:   auth.AuditEntry(r, "", "", "", &b.ID),
		})
		switch {
		case errors.Is(err, approvals.ErrNoApprover):
			ok, err := a.Approvals.ShortcutAllowed(ctx, tx)
			if err != nil {
				return err
			}
			if !ok {
				return httpx.Errorf(http.StatusConflict, "no_approver",
					"a second admin has to approve new keys, and nobody else can right now")
			}
			if err := tx.ActivateAPIKey(ctx, k.ID); err != nil {
				return err
			}
			k.Active = true
		case err != nil:
			return err
		default:
			pending = &ap
			if err := tx.SetAPIKeyApproval(ctx, k.ID, ap.ID); err != nil {
				return err
			}
			k.ApprovalID = &ap.ID
		}
		return tx.Audit(ctx, auth.AuditEntry(r, "api_key.created", "api_key", prefix, &b.ID), nil,
			map[string]any{"name": in.Name, "scopes": in.Scopes, "ip_allowlist": in.IPAllowlist, "expires_at": k.ExpiresAt, "awaiting_approval": pending != nil})
	})
	if err != nil {
		return err
	}
	v := a.view(k)
	v.Approval = pending
	// The secret is only ever in this response; only its hash is stored.
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"key": v, "secret": secret})
	return nil
}

func (a *Admin) revokeKey(w http.ResponseWriter, r *http.Request) error {
	b, p, err := brands.WithRole(r, a.Store, store.RoleBrandAdmin)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return httpx.ErrNotFound
	}
	ctx := r.Context()
	var k store.APIKey
	err = a.Store.InTx(ctx, func(tx *store.Store) error {
		if k, err = tx.APIKeyByID(ctx, b.ID, id); err != nil {
			return err
		}
		if err := tx.RevokeAPIKey(ctx, b.ID, id, &p.User.ID); err != nil {
			return err
		}
		k, _ = tx.APIKeyByID(ctx, b.ID, id)
		return tx.Audit(ctx, auth.AuditEntry(r, "api_key.revoked", "api_key", k.Prefix, &b.ID), nil, nil)
	})
	if errors.Is(err, store.ErrNotFound) {
		return httpx.Errorf(http.StatusNotFound, "not_found", "no such key, or it's already revoked")
	}
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, a.view(k))
	return nil
}

func (a *Admin) calls(w http.ResponseWriter, r *http.Request) error {
	b, _, err := brands.WithRole(r, a.Store, store.RoleBrandAdmin)
	if err != nil {
		return err
	}
	calls, err := a.Store.RecentAPICalls(r.Context(), b.ID, 100)
	if err != nil {
		return err
	}
	if calls == nil {
		calls = []store.APICall{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"calls": calls})
	return nil
}

// --- consent wordings ---

func (a *Admin) listWordings(w http.ResponseWriter, r *http.Request) error {
	b, _, err := brands.WithRole(r, a.Store, store.RoleViewer)
	if err != nil {
		return err
	}
	list, err := a.Store.ListWordings(r.Context(), b.ID)
	if err != nil {
		return err
	}
	if list == nil {
		list = []store.ConsentWording{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"wordings": list})
	return nil
}

func (a *Admin) createWording(w http.ResponseWriter, r *http.Request) error {
	b, p, err := brands.WithRole(r, a.Store, store.RoleBrandAdmin)
	if err != nil {
		return err
	}
	var in struct {
		Label string `json:"label"`
		Text  string `json:"text"`
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	in.Label, in.Text = strings.TrimSpace(in.Label), strings.TrimSpace(in.Text)
	if in.Label == "" || len([]rune(in.Label)) > 80 {
		return bad("invalid_label", "give it a short name, e.g. “Footer signup 2026”")
	}
	if n := len([]rune(in.Text)); n < 10 || n > 2000 {
		return bad("invalid_text", "the wording is the exact sentence people agree to (10 to 2,000 characters)")
	}
	ctx := r.Context()
	var cw store.ConsentWording
	err = a.Store.InTx(ctx, func(tx *store.Store) error {
		if cw, err = tx.CreateWording(ctx, b.ID, in.Label, in.Text, p.User.ID); err != nil {
			return err
		}
		return tx.Audit(ctx, auth.AuditEntry(r, "consent_wording.created", "consent_wording", cw.ID.String(), &b.ID), nil,
			map[string]string{"label": cw.Label, "text": cw.Text})
	})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, cw)
	return nil
}

func (a *Admin) retireWording(w http.ResponseWriter, r *http.Request) error {
	b, _, err := brands.WithRole(r, a.Store, store.RoleBrandAdmin)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return httpx.ErrNotFound
	}
	ctx := r.Context()
	var cw store.ConsentWording
	err = a.Store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.RetireWording(ctx, b.ID, id); err != nil {
			return err
		}
		if cw, err = tx.WordingByID(ctx, b.ID, id); err != nil {
			return err
		}
		return tx.Audit(ctx, auth.AuditEntry(r, "consent_wording.retired", "consent_wording", id.String(), &b.ID), nil,
			map[string]string{"label": cw.Label})
	})
	if errors.Is(err, store.ErrNotFound) {
		return httpx.Errorf(http.StatusNotFound, "not_found", "no such wording, or it's already retired")
	}
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, cw)
	return nil
}

// --- re-permission ---

type repermissionView struct {
	On          bool `json:"on"`
	Quarantined int  `json:"quarantined"`
	SentToday   int  `json:"sent_today"`
	PerDay      int  `json:"per_day"`
}

func (a *Admin) repermissionState(ctx context.Context, b store.Brand) (repermissionView, error) {
	v := repermissionView{On: b.RepermissionOn, PerDay: int(a.Limit(ctx, limits.RepermissionEmailsPerDay))}
	st, err := a.Store.ContactStats(ctx, b.ID)
	if err != nil {
		return v, err
	}
	v.Quarantined = st.Quarantined
	y, m, d := a.clock().UTC().Date()
	v.SentToday, err = a.Store.RepermissionSentSince(ctx, b.ID, time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
	return v, err
}

func (a *Admin) repermission(w http.ResponseWriter, r *http.Request) error {
	b, _, err := brands.WithRole(r, a.Store, store.RoleViewer)
	if err != nil {
		return err
	}
	v, err := a.repermissionState(r.Context(), b)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, v)
	return nil
}

func (a *Admin) setRepermission(w http.ResponseWriter, r *http.Request) error {
	b, _, err := brands.WithRole(r, a.Store, store.RoleBrandAdmin)
	if err != nil {
		return err
	}
	var in struct {
		On bool `json:"on"`
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	err = a.Store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.SetRepermission(ctx, b.ID, in.On); err != nil {
			return err
		}
		return tx.Audit(ctx, auth.AuditEntry(r, map[bool]string{true: "repermission.on", false: "repermission.off"}[in.On], "brand", b.Slug, &b.ID),
			map[string]bool{"on": b.RepermissionOn}, map[string]bool{"on": in.On})
	})
	if err != nil {
		return err
	}
	b.RepermissionOn = in.On
	v, err := a.repermissionState(ctx, b)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, v)
	return nil
}

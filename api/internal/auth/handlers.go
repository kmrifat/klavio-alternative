package auth

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// Routes registers the auth endpoints under /admin/v1.
func (s *Service) Routes(mux *http.ServeMux) {
	h := func(f httpx.HandlerFunc) http.HandlerFunc { return httpx.Handle(s.log, f) }

	mux.HandleFunc("GET /admin/v1/me", h(s.me))
	mux.HandleFunc("POST /admin/v1/auth/login/begin", h(s.loginBegin))
	mux.HandleFunc("POST /admin/v1/auth/login/finish", h(s.loginFinish))
	mux.HandleFunc("POST /admin/v1/auth/logout", h(s.logout))
	mux.HandleFunc("POST /admin/v1/auth/stepup/begin", h(s.stepUpBegin))
	mux.HandleFunc("POST /admin/v1/auth/stepup/finish", h(s.stepUpFinish))
	mux.HandleFunc("GET /admin/v1/invites/{token}", h(s.inviteGet))
	mux.HandleFunc("POST /admin/v1/invites/{token}/begin", h(s.inviteBegin))
	mux.HandleFunc("POST /admin/v1/invites/{token}/finish", h(s.inviteFinish))
}

type meResponse struct {
	User struct {
		ID          uuid.UUID         `json:"id"`
		Email       string            `json:"email"`
		Name        string            `json:"name"`
		CompanyRole store.CompanyRole `json:"company_role"`
	} `json:"user"`
	BrandRoles     store.BrandRoles `json:"brand_roles"`
	SteppedUpUntil *time.Time       `json:"stepped_up_until"`
}

func (s *Service) meBody(p *Principal) meResponse {
	var m meResponse
	m.User.ID, m.User.Email, m.User.Name, m.User.CompanyRole = p.User.ID, p.User.Email, p.User.Name, p.User.CompanyRole
	m.BrandRoles = p.Roles
	if m.BrandRoles == nil {
		m.BrandRoles = store.BrandRoles{}
	}
	if p.SteppedUp(s.now()) {
		until := p.SteppedUpAt.Add(StepUpWindow)
		m.SteppedUpUntil = &until
	}
	return m
}

func (s *Service) me(w http.ResponseWriter, r *http.Request) error {
	p, err := Require(r)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, s.meBody(p))
	return nil
}

type beginResponse struct {
	Ceremony string `json:"ceremony"`
	Options  any    `json:"options"`
}

// --- sign in ---

func (s *Service) loginBegin(w http.ResponseWriter, r *http.Request) error {
	assertion, sd, err := s.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return err
	}
	id, err := s.saveCeremony(r.Context(), "login", "", sd)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, beginResponse{Ceremony: id, Options: assertion})
	return nil
}

var errPasskeyFailed = httpx.Errorf(http.StatusUnauthorized, "passkey_failed", "that passkey didn't work for this app")

var errFrozen = httpx.Errorf(http.StatusForbidden, "account_frozen",
	"this account is frozen after unusual activity; an owner has been alerted and can unfreeze it")

func (s *Service) loginFinish(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	sd, err := s.takeCeremony(ctx, r, "login", "")
	if err != nil {
		return err
	}

	var user store.User
	lookup := func(_, userHandle []byte) (webauthn.User, error) {
		u, err := s.store.UserByWebAuthnID(ctx, userHandle)
		if err != nil {
			return nil, err
		}
		user = u
		return s.loadWAUser(ctx, u)
	}
	_, cred, err := s.wa.FinishPasskeyLogin(lookup, sd, r)
	if err != nil {
		s.log.WarnContext(ctx, "passkey sign-in failed", "err", err, "ip", httpx.ClientIP(r))
		return errPasskeyFailed
	}
	if ae := accessEmail(ctx); ae != "" && !strings.EqualFold(ae, user.Email) {
		return httpx.Errorf(http.StatusForbidden, "access_identity_mismatch",
			"you're signed in to Cloudflare as %s but this passkey belongs to %s", ae, user.Email)
	}
	if user.FrozenAt != nil {
		return errFrozen
	}
	// The anomaly check commits on its own: a freeze must stick even though
	// no session is created.
	if s.guard != nil {
		var frozen bool
		err := s.store.InTx(ctx, func(tx *store.Store) error {
			var err error
			frozen, err = s.guard.CheckSignIn(ctx, tx, user, httpx.ClientIP(r), httpx.ClientCountry(r), hex.EncodeToString(cred.ID))
			return err
		})
		if err != nil {
			return err
		}
		if frozen {
			return errFrozen
		}
	}

	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.UpdatePasskeyAfterLogin(ctx, *cred); err != nil {
			return err
		}
		if err := s.startSession(ctx, tx, w, r, user.ID); err != nil {
			return err
		}
		e := AuditEntry(r, "auth.signed_in", "user", user.ID.String(), nil)
		e.ActorID, e.ActorEmail = &user.ID, user.Email
		return tx.Audit(ctx, e, nil, nil)
	})
	if err != nil {
		return err
	}
	return s.respondMe(ctx, w, user.ID)
}

func (s *Service) respondMe(ctx context.Context, w http.ResponseWriter, userID uuid.UUID) error {
	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	roles, err := s.store.MembershipsForUser(ctx, userID)
	if err != nil {
		return err
	}
	now := s.now()
	httpx.WriteJSON(w, http.StatusOK, s.meBody(&Principal{User: u, Roles: roles, SteppedUpAt: &now}))
	return nil
}

func (s *Service) logout(w http.ResponseWriter, r *http.Request) error {
	if p := FromContext(r.Context()); p != nil {
		if err := s.store.DeleteSession(r.Context(), p.SessionHash); err != nil {
			return err
		}
	}
	s.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// --- step-up (re-confirm with passkey before a sensitive action) ---

func (s *Service) stepUpBegin(w http.ResponseWriter, r *http.Request) error {
	p, err := Require(r)
	if err != nil {
		return err
	}
	wu, err := s.loadWAUser(r.Context(), p.User)
	if err != nil {
		return err
	}
	assertion, sd, err := s.wa.BeginLogin(wu, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return err
	}
	id, err := s.saveCeremony(r.Context(), "stepup", hex.EncodeToString(p.SessionHash), sd)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, beginResponse{Ceremony: id, Options: assertion})
	return nil
}

func (s *Service) stepUpFinish(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p, err := Require(r)
	if err != nil {
		return err
	}
	sd, err := s.takeCeremony(ctx, r, "stepup", hex.EncodeToString(p.SessionHash))
	if err != nil {
		return err
	}
	wu, err := s.loadWAUser(ctx, p.User)
	if err != nil {
		return err
	}
	cred, err := s.wa.FinishLogin(wu, sd, r)
	if err != nil {
		s.log.WarnContext(ctx, "step-up failed", "err", err, "user", p.User.ID)
		return errPasskeyFailed
	}
	now := s.now()
	if err := s.store.UpdatePasskeyAfterLogin(ctx, *cred); err != nil {
		return err
	}
	if err := s.store.MarkSteppedUp(ctx, p.SessionHash, now); err != nil {
		return err
	}
	p.SteppedUpAt = &now
	httpx.WriteJSON(w, http.StatusOK, s.meBody(p))
	return nil
}

// --- accepting an invite ---

func (s *Service) openInvite(r *http.Request) (store.Invite, error) {
	inv, err := s.store.OpenInvite(r.Context(), hashToken(r.PathValue("token")))
	if errors.Is(err, store.ErrNotFound) {
		return inv, httpx.Errorf(http.StatusNotFound, "invite_invalid", "this invite link is invalid, used or expired")
	}
	if err != nil {
		return inv, err
	}
	if ae := accessEmail(r.Context()); ae != "" && !strings.EqualFold(ae, inv.Email) {
		return inv, httpx.Errorf(http.StatusForbidden, "access_identity_mismatch",
			"this invite is for %s but you're signed in to Cloudflare as %s", inv.Email, ae)
	}
	if grantsAdmin(inv) && inv.ApprovalID == nil {
		// Sent without approval while the company had one admin. Once there
		// have been two, such an invite would skip the two-person rule.
		since, err := s.store.TwoAdminsSince(r.Context())
		if err != nil {
			return inv, err
		}
		if since != nil && since.After(inv.CreatedAt) {
			return inv, httpx.Errorf(http.StatusConflict, "invite_needs_approval",
				"this invite gives admin rights and was sent before a second admin joined; ask an owner to send a new one (it will need approval)")
		}
	}
	switch st, err := s.store.InviteApprovalStatus(r.Context(), inv); {
	case err != nil:
		return inv, err
	case st == store.ApprovalPending:
		return inv, httpx.Errorf(http.StatusConflict, "invite_awaiting_approval",
			"this invite gives admin rights, so a second admin has to approve it first — try again later")
	case st != "" && st != store.ApprovalApproved:
		return inv, httpx.Errorf(http.StatusNotFound, "invite_invalid", "this invite link is invalid, used or expired")
	}
	return inv, nil
}

func (s *Service) inviteGet(w http.ResponseWriter, r *http.Request) error {
	inv, err := s.openInvite(r)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"email": inv.Email, "name": inv.Name, "expires_at": inv.ExpiresAt})
	return nil
}

// grantsAdmin: owner, approver or brand admin rights.
func grantsAdmin(inv store.Invite) bool {
	if inv.CompanyRole == store.CompanyOwner || inv.CompanyRole == store.CompanyApprover {
		return true
	}
	for _, r := range inv.BrandRoles {
		if r == store.RoleBrandAdmin {
			return true
		}
	}
	return false
}

func inviteWAUser(inv store.Invite) *waUser {
	return &waUser{id: inv.WebAuthnID, name: inv.Email, displayName: inv.Name}
}

func (s *Service) inviteBegin(w http.ResponseWriter, r *http.Request) error {
	inv, err := s.openInvite(r)
	if err != nil {
		return err
	}
	creation, sd, err := s.wa.BeginRegistration(inviteWAUser(inv), registrationOptions(nil)...)
	if err != nil {
		return err
	}
	id, err := s.saveCeremony(r.Context(), "invite", inv.ID.String(), sd)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, beginResponse{Ceremony: id, Options: creation})
	return nil
}

func (s *Service) inviteFinish(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	inv, err := s.openInvite(r)
	if err != nil {
		return err
	}
	sd, err := s.takeCeremony(ctx, r, "invite", inv.ID.String())
	if err != nil {
		return err
	}
	cred, err := s.wa.FinishRegistration(inviteWAUser(inv), sd, r)
	if err != nil {
		s.log.WarnContext(ctx, "passkey registration failed", "err", err, "invite", inv.ID)
		return httpx.Errorf(http.StatusBadRequest, "passkey_failed", "the passkey couldn't be created, please try again")
	}

	var user store.User
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.AcceptInvite(ctx, inv.ID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return httpx.Errorf(http.StatusConflict, "invite_used", "this invite was already used")
			}
			return err
		}
		user, err = tx.CreateUser(ctx, inv.Email, inv.Name, inv.CompanyRole, inv.WebAuthnID)
		if errors.Is(err, store.ErrConflict) {
			return httpx.Errorf(http.StatusConflict, "already_member", "%s already has an account", inv.Email)
		}
		if err != nil {
			return err
		}
		brands, err := tx.ListBrands(ctx)
		if err != nil {
			return err
		}
		for _, b := range brands { // brands deleted since the invite are skipped
			if role := inv.BrandRoles[b.ID]; role != "" {
				if err := tx.SetMembership(ctx, user.ID, b.ID, role); err != nil {
					return err
				}
			}
		}
		if err := tx.AddPasskey(ctx, user.ID, *cred); err != nil {
			return err
		}
		if err := tx.RecordSignIn(ctx, user.ID, httpx.ClientIP(r), httpx.ClientCountry(r), hex.EncodeToString(cred.ID)); err != nil {
			return err
		}
		if err := s.startSession(ctx, tx, w, r, user.ID); err != nil {
			return err
		}
		e := AuditEntry(r, "user.joined", "user", user.ID.String(), nil)
		e.ActorID, e.ActorEmail = &user.ID, user.Email
		return tx.Audit(ctx, e, nil, map[string]any{"company_role": inv.CompanyRole, "brand_roles": inv.BrandRoles})
	})
	if err != nil {
		return err
	}
	return s.respondMe(ctx, w, user.ID)
}

// Package auth handles sign-in with passkeys, sessions, invites, step-up
// confirmation and the per-request Principal.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

const (
	sessionTTL      = 12 * time.Hour
	inviteTTL       = 7 * 24 * time.Hour
	touchSessionGap = time.Minute
)

type Config struct {
	RPID          string   // e.g. "yourco.com" or "localhost"
	RPDisplayName string   // shown by the browser's passkey dialog
	RPOrigins     []string // the web app's origins
	SecureCookies bool     // true in production (HTTPS)
	AppURL        string   // base URL of the web app, for invite links
}

type Service struct {
	store      *store.Store
	wa         *webauthn.WebAuthn
	ceremonies CeremonyStore
	access     *AccessVerifier // nil when Cloudflare Access isn't configured
	cfg        Config
	log        *slog.Logger
	now        func() time.Time
}

func NewService(st *store.Store, ceremonies CeremonyStore, access *AccessVerifier, cfg Config, log *slog.Logger) (*Service, error) {
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          cfg.RPID,
		RPDisplayName: cfg.RPDisplayName,
		RPOrigins:     cfg.RPOrigins,
	})
	if err != nil {
		return nil, fmt.Errorf("webauthn config: %w", err)
	}
	return &Service{store: st, wa: wa, ceremonies: ceremonies, access: access, cfg: cfg, log: log, now: time.Now}, nil
}

func (s *Service) cookieName() string {
	if s.cfg.SecureCookies {
		return "__Host-sid" // browser enforces Secure, Path=/ and no Domain
	}
	return "sid"
}

// --- middleware ---

// Middleware verifies Cloudflare Access (when configured) and loads the
// Principal from the session cookie. It never rejects for a missing session;
// handlers decide with Require.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if s.access != nil {
			email, err := s.access.Email(r)
			if err != nil {
				s.log.WarnContext(ctx, "cloudflare access check failed", "err", err)
				httpx.WriteJSON(w, http.StatusForbidden, map[string]*httpx.Error{"error": {Code: "access_required", Message: "open this app through Cloudflare Access"}})
				return
			}
			ctx = context.WithValue(ctx, accessEmailKey{}, email)
		}

		if c, err := r.Cookie(s.cookieName()); err == nil && c.Value != "" {
			p, err := s.principalForToken(ctx, c.Value)
			switch {
			case err == nil:
				ctx = withPrincipal(ctx, p)
			case !errors.Is(err, store.ErrNotFound):
				s.log.ErrorContext(ctx, "loading session", "err", err)
			}
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Service) principalForToken(ctx context.Context, token string) (*Principal, error) {
	hash := hashToken(token)
	sess, err := s.store.ActiveSession(ctx, hash)
	if err != nil {
		return nil, err
	}
	user, err := s.store.UserByID(ctx, sess.UserID)
	if err != nil {
		return nil, err
	}
	// With Access on, the Google account must match the app account.
	if ae := accessEmail(ctx); ae != "" && !strings.EqualFold(ae, user.Email) {
		return nil, store.ErrNotFound
	}
	roles, err := s.store.MembershipsForUser(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	if s.now().Sub(sess.LastSeenAt) > touchSessionGap {
		if err := s.store.TouchSession(ctx, hash); err != nil {
			s.log.WarnContext(ctx, "touch session", "err", err)
		}
		if err := s.store.TouchUser(ctx, user.ID); err != nil {
			s.log.WarnContext(ctx, "touch user", "err", err)
		}
	}
	return &Principal{User: user, Roles: roles, SessionHash: hash, SteppedUpAt: sess.SteppedUpAt}, nil
}

// Require returns the principal or ErrUnauthenticated.
func Require(r *http.Request) (*Principal, error) {
	p := FromContext(r.Context())
	if p == nil {
		return nil, httpx.ErrUnauthenticated
	}
	return p, nil
}

// RequireStepUp returns the principal if they confirmed their passkey recently.
func (s *Service) RequireStepUp(r *http.Request) (*Principal, error) {
	p, err := Require(r)
	if err != nil {
		return nil, err
	}
	if !p.SteppedUp(s.now()) {
		return nil, httpx.ErrStepUpRequired
	}
	return p, nil
}

// AuditEntry pre-fills who/where for an audit log entry.
func AuditEntry(r *http.Request, action, targetType, targetID string, brandID *uuid.UUID) store.AuditEntry {
	e := store.AuditEntry{
		Action: action, TargetType: targetType, TargetID: targetID, BrandID: brandID,
		IP: httpx.ClientIP(r), UserAgent: r.UserAgent(),
	}
	if p := FromContext(r.Context()); p != nil {
		e.ActorID, e.ActorEmail = &p.User.ID, p.User.Email
	}
	return e
}

// --- invites ---

type InviteInput struct {
	Email       string
	Name        string
	CompanyRole store.CompanyRole
	BrandRoles  store.BrandRoles
}

func (in *InviteInput) normalize() error {
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.Name = strings.TrimSpace(in.Name)
	if addr, err := mail.ParseAddress(in.Email); err != nil || addr.Address != in.Email {
		return httpx.Errorf(http.StatusBadRequest, "invalid_email", "%q is not a valid email address", in.Email)
	}
	if in.Name == "" {
		return httpx.Errorf(http.StatusBadRequest, "invalid_name", "name is required")
	}
	if in.CompanyRole == "" {
		in.CompanyRole = store.CompanyMember
	}
	if !in.CompanyRole.Valid() {
		return httpx.Errorf(http.StatusBadRequest, "invalid_role", "unknown company role %q", in.CompanyRole)
	}
	for _, r := range in.BrandRoles {
		if !r.Valid() {
			return httpx.Errorf(http.StatusBadRequest, "invalid_role", "unknown brand role %q", r)
		}
	}
	return nil
}

// CreateInvite stores a one-time invite and returns its link. Only the token's
// hash is stored, so the link can't be shown again.
func (s *Service) CreateInvite(ctx context.Context, tx *store.Store, by *uuid.UUID, in InviteInput) (store.Invite, string, error) {
	if err := in.normalize(); err != nil {
		return store.Invite{}, "", err
	}
	token, err := randomToken(32)
	if err != nil {
		return store.Invite{}, "", err
	}
	waID, err := randomBytes(64)
	if err != nil {
		return store.Invite{}, "", err
	}
	if err := tx.DeleteExpiredOpenInvite(ctx, in.Email); err != nil {
		return store.Invite{}, "", err
	}
	inv, err := tx.CreateInvite(ctx, store.Invite{
		TokenHash: hashToken(token), Email: in.Email, Name: in.Name,
		CompanyRole: in.CompanyRole, BrandRoles: in.BrandRoles, WebAuthnID: waID,
		InvitedBy: by, ExpiresAt: s.now().Add(inviteTTL),
	})
	if errors.Is(err, store.ErrConflict) {
		return store.Invite{}, "", httpx.Errorf(http.StatusConflict, "already_invited", "%s already has an account or an open invite", in.Email)
	}
	if err != nil {
		return store.Invite{}, "", err
	}
	return inv, strings.TrimSuffix(s.cfg.AppURL, "/") + "/invite/" + token, nil
}

// --- passkey helpers ---

type waUser struct {
	id          []byte
	name        string
	displayName string
	creds       []webauthn.Credential
}

func (u *waUser) WebAuthnID() []byte                         { return u.id }
func (u *waUser) WebAuthnName() string                       { return u.name }
func (u *waUser) WebAuthnDisplayName() string                { return u.displayName }
func (u *waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func (s *Service) loadWAUser(ctx context.Context, u store.User) (*waUser, error) {
	keys, err := s.store.PasskeysForUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	wu := &waUser{id: u.WebAuthnID, name: u.Email, displayName: u.Name}
	for _, k := range keys {
		wu.creds = append(wu.creds, k.Credential)
	}
	return wu, nil
}

func registrationOptions(existing []webauthn.Credential) []webauthn.RegistrationOption {
	excl := make([]protocol.CredentialDescriptor, 0, len(existing))
	for _, c := range existing {
		excl = append(excl, c.Descriptor())
	}
	return []webauthn.RegistrationOption{
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification:   protocol.VerificationRequired,
		}),
		webauthn.WithExclusions(excl),
	}
}

// startSession creates a session row and sets the cookie. A fresh sign-in
// counts as a step-up.
func (s *Service) startSession(ctx context.Context, tx *store.Store, w http.ResponseWriter, r *http.Request, userID uuid.UUID) error {
	token, err := randomToken(32)
	if err != nil {
		return err
	}
	now := s.now()
	if err := tx.CreateSession(ctx, store.Session{
		TokenHash: hashToken(token), UserID: userID, ExpiresAt: now.Add(sessionTTL),
		SteppedUpAt: &now, IP: httpx.ClientIP(r), UserAgent: r.UserAgent(),
	}); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(), Value: token, Path: "/",
		MaxAge: int(sessionTTL.Seconds()), HttpOnly: true, Secure: s.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (s *Service) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Service) saveCeremony(ctx context.Context, kind, ref string, sd *webauthn.SessionData) (string, error) {
	id, err := randomToken(16)
	if err != nil {
		return "", err
	}
	return id, s.ceremonies.Put(ctx, id, ceremony{Session: *sd, Kind: kind, Ref: ref})
}

func (s *Service) takeCeremony(ctx context.Context, r *http.Request, kind, ref string) (webauthn.SessionData, error) {
	c, err := s.ceremonies.Take(ctx, r.URL.Query().Get("ceremony"))
	if errors.Is(err, errCeremonyNotFound) || (err == nil && (c.Kind != kind || c.Ref != ref)) {
		return webauthn.SessionData{}, httpx.Errorf(http.StatusBadRequest, "ceremony_expired", "this passkey request expired, please try again")
	}
	return c.Session, err
}

// --- tokens ---

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	return b, err
}

func randomToken(n int) (string, error) {
	b, err := randomBytes(n)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// Package agents manages the tokens that let a person's AI agent (Claude
// Code, Codex, OpenCode…) use the console through MCP. A token acts as the
// person who made it, with at most their role and never a passkey
// confirmation: anything that needs one stays with people.
package agents

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// MaxDays: tokens expire after at most 90 days.
const MaxDays = 90

const tokenPrefix = "kag_"

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// newToken returns the full secret (shown once) and its public prefix.
func newToken() (secret, prefix string, err error) {
	var id [5]byte
	var sec [32]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", "", err
	}
	if _, err := rand.Read(sec[:]); err != nil {
		return "", "", err
	}
	prefix = tokenPrefix + strings.ToLower(b32.EncodeToString(id[:]))
	return prefix + "_" + strings.ToLower(b32.EncodeToString(sec[:])), prefix, nil
}

func hash(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

type stepUpper interface {
	RequireStepUp(r *http.Request) (*auth.Principal, error)
}

type Service struct {
	Store *store.Store
	Auth  stepUpper
	Log   *slog.Logger
	Now   func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ErrInvalid: the token is unknown, revoked, expired, or its person is
// frozen. Callers answer 401 without saying which.
var ErrInvalid = errors.New("agents: invalid token")

// Principal is who a token acts as: its person, their current roles, and
// the token itself (so step-up and approvals refuse it).
func (s *Service) Principal(ctx context.Context, secret string) (*auth.Principal, error) {
	if !strings.HasPrefix(secret, tokenPrefix) {
		return nil, ErrInvalid
	}
	t, err := s.Store.AgentTokenByHash(ctx, hash(secret))
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrInvalid
	}
	if err != nil {
		return nil, err
	}
	if !t.Usable(s.now()) {
		return nil, ErrInvalid
	}
	u, err := s.Store.UserByID(ctx, t.UserID)
	if err != nil {
		return nil, err
	}
	if u.FrozenAt != nil {
		return nil, ErrInvalid
	}
	roles, err := s.Store.MembershipsForUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	if err := s.Store.TouchAgentToken(ctx, t.ID); err != nil {
		s.Log.WarnContext(ctx, "touch agent token", "err", err)
	}
	return &auth.Principal{User: u, Roles: roles, Agent: &t}, nil
}

func (s *Service) Routes(mux *http.ServeMux) {
	f := func(fn httpx.HandlerFunc) http.HandlerFunc { return httpx.Handle(s.Log, fn) }
	mux.HandleFunc("GET /admin/v1/agent-tokens", f(s.list))
	mux.HandleFunc("POST /admin/v1/agent-tokens", f(s.create))
	mux.HandleFunc("POST /admin/v1/agent-tokens/{id}/revoke", f(s.revoke))
}

type tokenView struct {
	store.AgentToken
	State string `json:"state"` // active | expired | revoked
}

func (s *Service) view(t store.AgentToken) tokenView {
	v := tokenView{AgentToken: t, State: "active"}
	switch {
	case t.RevokedAt != nil:
		v.State = "revoked"
	case !s.now().Before(t.ExpiresAt):
		v.State = "expired"
	}
	return v
}

// list: your own tokens. An agent can see the tokens of its person too.
func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	p, err := auth.Require(r)
	if err != nil {
		return err
	}
	ts, err := s.Store.AgentTokensForUser(r.Context(), p.User.ID)
	if err != nil {
		return err
	}
	out := []tokenView{}
	for _, t := range ts {
		out = append(out, s.view(t))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"tokens": out})
	return nil
}

// create makes a token for yourself, with a passkey confirmation. The
// secret is in this answer only.
func (s *Service) create(w http.ResponseWriter, r *http.Request) error {
	p, err := s.Auth.RequireStepUp(r)
	if err != nil {
		return err
	}
	var in struct {
		Name string `json:"name"`
		Days int    `json:"expires_in_days"`
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	name := strings.Join(strings.Fields(in.Name), " ")
	switch {
	case name == "":
		return httpx.Errorf(http.StatusBadRequest, "name_required", "name the agent, e.g. \"Claude Code on my laptop\"")
	case utf8.RuneCountInString(name) > 60:
		return httpx.Errorf(http.StatusBadRequest, "name_too_long", "keep the name under 60 characters")
	case in.Days < 1 || in.Days > MaxDays:
		return httpx.Errorf(http.StatusBadRequest, "invalid_expiry", "a token lasts 1 to %d days", MaxDays)
	}
	ctx := r.Context()
	var t store.AgentToken
	var secret string
	err = s.Store.InTx(ctx, func(tx *store.Store) error {
		var err error
		t, secret, err = s.Mint(ctx, tx, p.User.ID, name, in.Days, nil, auth.AuditEntry(r, "", "", "", nil))
		return err
	})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"token": s.view(t), "secret": secret})
	return nil
}

// Mint makes a token for a user, inside tx, and audits it. clientID is set
// for tokens a sign-in (OAuth) made for an AI client. The secret is
// returned once and never stored.
func (s *Service) Mint(ctx context.Context, tx *store.Store, userID uuid.UUID, name string, days int, clientID *string, audit store.AuditEntry) (store.AgentToken, string, error) {
	secret, prefix, err := newToken()
	if err != nil {
		return store.AgentToken{}, "", err
	}
	t, err := tx.CreateAgentToken(ctx, store.AgentToken{UserID: userID, Name: name, Prefix: prefix, TokenHash: hash(secret),
		ExpiresAt: s.now().Add(time.Duration(days) * 24 * time.Hour), OAuthClientID: clientID})
	if err != nil {
		return t, "", err
	}
	audit.Action, audit.TargetType, audit.TargetID = "agent_token.created", "agent_token", prefix
	after := map[string]any{"name": name, "expires_at": t.ExpiresAt}
	if clientID != nil {
		after["oauth_client_id"] = *clientID
	}
	return t, secret, tx.Audit(ctx, audit, nil, after)
}

// revoke: any of your tokens, at once. No passkey needed to switch one off,
// and an agent may revoke its own person's tokens (including itself).
func (s *Service) revoke(w http.ResponseWriter, r *http.Request) error {
	p, err := auth.Require(r)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return httpx.ErrNotFound
	}
	ctx := r.Context()
	var t store.AgentToken
	err = s.Store.InTx(ctx, func(tx *store.Store) error {
		var err error
		if t, err = tx.RevokeAgentToken(ctx, p.User.ID, id); errors.Is(err, store.ErrNotFound) {
			return httpx.ErrNotFound
		} else if err != nil {
			return err
		}
		return tx.Audit(ctx, auth.AuditEntry(r, "agent_token.revoked", "agent_token", t.Prefix, nil), nil, map[string]any{"name": t.Name})
	})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"token": s.view(t)})
	return nil
}

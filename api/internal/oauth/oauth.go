// Package oauth lets AI clients that sign in with OAuth (claude.ai and
// Claude Desktop connectors) connect to the MCP server, as MCP's
// authorization spec describes:
//
//   - discovery: protected resource metadata (RFC 9728) and authorization
//     server metadata (RFC 8414);
//   - the client registers itself (RFC 7591), as a public client;
//   - /oauth/authorize sends the person to the console, where they approve
//     with their passkey (or deny);
//   - /oauth/token swaps the one-time code (PKCE S256, 5 minutes) for an
//     agent token for that person, the same kind Settings → AI agents makes.
//
// There are no refresh tokens: the token lasts TokenDays, then the person
// connects again. They see and revoke it in Settings → AI agents.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/agents"
	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

const (
	// TokenDays: how long a connection's token lasts.
	TokenDays = 90
	// DecideWithin: how long the person has to approve.
	DecideWithin = 10 * time.Minute
	// CodeLife: how long the client has to swap the code.
	CodeLife = 5 * time.Minute
)

type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

type stepUpper interface {
	RequireStepUp(r *http.Request) (*auth.Principal, error)
}

type Service struct {
	Store   *store.Store
	Agents  *agents.Service
	Auth    stepUpper
	Limiter Limiter
	Log     *slog.Logger
	// APIURL is this API's public address; the MCP server is APIURL/mcp.
	APIURL string
	// AppURL is the web app, where people approve.
	AppURL string
	Now    func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Resource is the MCP server's identifier: what tokens are for.
func (s *Service) Resource() string { return s.APIURL + "/mcp" }

// ResourceMetadataURL is where MCP's 401 answers point clients.
func (s *Service) ResourceMetadataURL() string {
	return s.APIURL + "/.well-known/oauth-protected-resource/mcp"
}

// PublicRoutes: discovery, registration, authorize and token, on the API's
// public address (no session, no CSRF: OAuth clients aren't browsers on our
// origin).
func (s *Service) PublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.resourceMeta)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.resourceMeta)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.serverMeta)
	mux.HandleFunc("POST /oauth/register", s.register)
	mux.HandleFunc("OPTIONS /oauth/register", preflight)
	mux.HandleFunc("GET /oauth/authorize", s.authorize)
	mux.HandleFunc("POST /oauth/token", s.token)
	mux.HandleFunc("OPTIONS /oauth/token", preflight)
}

// AdminRoutes: the console's approval screen.
func (s *Service) AdminRoutes(mux *http.ServeMux) {
	f := func(fn httpx.HandlerFunc) http.HandlerFunc { return httpx.Handle(s.Log, fn) }
	mux.HandleFunc("GET /admin/v1/oauth/grants/{id}", f(s.getGrant))
	mux.HandleFunc("POST /admin/v1/oauth/grants/{id}/approve", f(s.approve))
	mux.HandleFunc("POST /admin/v1/oauth/grants/{id}/deny", f(s.deny))
}

// --- discovery ---

func cors(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, MCP-Protocol-Version")
}

func preflight(w http.ResponseWriter, _ *http.Request) {
	cors(w)
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	cors(w)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Service) resourceMeta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 s.Resource(),
		"authorization_servers":    []string{s.APIURL},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "Mail Console",
	})
}

func (s *Service) serverMeta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         s.APIURL,
		"authorization_endpoint":                         s.APIURL + "/oauth/authorize",
		"token_endpoint":                                 s.APIURL + "/oauth/token",
		"registration_endpoint":                          s.APIURL + "/oauth/register",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"authorization_response_iss_parameter_supported": true,
	})
}

// oauthError is RFC 6749's error answer.
func oauthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

func (s *Service) limited(w http.ResponseWriter, r *http.Request, what string, n int, per time.Duration) bool {
	if s.Limiter == nil {
		return false
	}
	ok, err := s.Limiter.Allow(r.Context(), "oauth:"+what+":"+httpx.ClientIP(r), n, per)
	if err == nil && !ok {
		oauthError(w, http.StatusTooManyRequests, "slow_down", "too many requests; try again later")
		return true
	}
	return false
}

// --- registration (RFC 7591) ---

// validRedirect: https anywhere, or http on this computer (loopback), with
// no fragment.
func validRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Fragment != "" || u.User != nil {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		h := u.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	}
	return false
}

func randomID(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hash(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

func (s *Service) register(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "register", 20, time.Hour) {
		return
	}
	var in struct {
		ClientName              string   `json:"client_name"`
		RedirectURIs            []string `json:"redirect_uris"`
		GrantTypes              []string `json:"grant_types"`
		ResponseTypes           []string `json:"response_types"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "send the client's metadata as JSON")
		return
	}
	name := strings.Join(strings.Fields(in.ClientName), " ")
	if name == "" {
		name = "An AI client"
	}
	if len([]rune(name)) > 100 {
		name = string([]rune(name)[:100])
	}
	switch {
	case len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > 5:
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "give 1 to 5 redirect_uris")
		return
	case in.TokenEndpointAuthMethod != "" && in.TokenEndpointAuthMethod != "none":
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "only public clients (token_endpoint_auth_method none, with PKCE) are supported")
		return
	}
	for _, u := range in.RedirectURIs {
		if !validRedirect(u) {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris must be https, or http on localhost: "+u)
			return
		}
	}
	for _, g := range in.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "only the authorization_code grant is supported")
			return
		}
	}
	id, err := randomID(18)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	c, err := s.Store.CreateOAuthClient(r.Context(), store.OAuthClient{ID: "mcpc_" + id, Name: name, RedirectURIs: in.RedirectURIs})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id": c.ID, "client_name": c.Name, "redirect_uris": c.RedirectURIs, "client_id_issued_at": c.CreatedAt.Unix(),
		"grant_types": []string{"authorization_code"}, "response_types": []string{"code"}, "token_endpoint_auth_method": "none",
	})
}

func (s *Service) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.ErrorContext(r.Context(), "oauth", "err", err)
	oauthError(w, http.StatusInternalServerError, "server_error", "something went wrong")
}

// --- authorize ---

var errorPage = template.Must(template.New("error").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Can't connect</title><style>body{font:16px/1.5 system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem;color:#18181b}</style></head>
<body><h1>This connection can't go ahead</h1><p>{{.}}</p><p>Close this tab and try connecting again from your AI app.</p></body></html>`))

// back sends the person to the client with an error (only once the client
// and redirect are known to be genuine).
func back(w http.ResponseWriter, r *http.Request, redirect, state, code, desc string) {
	u, _ := url.Parse(redirect)
	q := u.Query()
	q.Set("error", code)
	q.Set("error_description", desc)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Service) authorize(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "authorize", 60, time.Minute) {
		return
	}
	q := r.URL.Query()
	c, err := s.Store.OAuthClientByID(r.Context(), q.Get("client_id"))
	redirect := q.Get("redirect_uri")
	if errors.Is(err, store.ErrNotFound) || (err == nil && !slices.Contains(c.RedirectURIs, redirect)) {
		// Never redirect to an address we can't vouch for.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_ = errorPage.Execute(w, "The app asking to connect isn't registered here, or asked to send you somewhere it didn't register.")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	state := q.Get("state")
	switch {
	case q.Get("response_type") != "code":
		back(w, r, redirect, state, "unsupported_response_type", "only response_type=code is supported")
		return
	case q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256":
		back(w, r, redirect, state, "invalid_request", "PKCE with code_challenge_method=S256 is required")
		return
	case q.Get("resource") != "" && strings.TrimSuffix(q.Get("resource"), "/") != s.Resource():
		back(w, r, redirect, state, "invalid_target", "this server only issues tokens for "+s.Resource())
		return
	}
	id, err := s.Store.CreateOAuthGrant(r.Context(), store.OAuthGrant{ClientID: c.ID, RedirectURI: redirect, State: state,
		CodeChallenge: q.Get("code_challenge"), ExpiresAt: s.now().Add(DecideWithin)})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, strings.TrimSuffix(s.AppURL, "/")+"/connect/"+id.String(), http.StatusFound)
}

// --- the console: approve or deny ---

type grantView struct {
	ID           uuid.UUID `json:"id"`
	ClientName   string    `json:"client_name"`
	RedirectHost string    `json:"redirect_host"` // where the person goes back to, e.g. claude.ai
	ExpiresAt    time.Time `json:"expires_at"`
	Status       string    `json:"status"` // pending | approved | denied | expired
	TokenDays    int       `json:"token_days"`
}

func (s *Service) view(g store.OAuthGrant) grantView {
	v := grantView{ID: g.ID, ClientName: g.ClientName, ExpiresAt: g.ExpiresAt, Status: "pending", TokenDays: TokenDays}
	if u, err := url.Parse(g.RedirectURI); err == nil {
		v.RedirectHost = u.Host
	}
	switch {
	case g.ApprovedAt != nil:
		v.Status = "approved"
	case g.DeniedAt != nil:
		v.Status = "denied"
	case !s.now().Before(g.ExpiresAt):
		v.Status = "expired"
	}
	return v
}

func grantID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return id, httpx.ErrNotFound
	}
	return id, nil
}

func (s *Service) getGrant(w http.ResponseWriter, r *http.Request) error {
	if _, err := auth.Require(r); err != nil {
		return err
	}
	id, err := grantID(r)
	if err != nil {
		return err
	}
	g, err := s.Store.OAuthGrantByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, s.view(g))
	return nil
}

var errNotPending = httpx.Errorf(http.StatusConflict, "not_pending", "this request was already answered or has expired; start again from your AI app")

// decide locks a pending grant and runs fn on it; fn returns where to send the person.
func (s *Service) decide(r *http.Request, fn func(tx *store.Store, g store.OAuthGrant) (string, error)) (string, error) {
	id, err := grantID(r)
	if err != nil {
		return "", err
	}
	var to string
	err = s.Store.InTx(r.Context(), func(tx *store.Store) error {
		g, err := tx.LockOAuthGrant(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		if !g.Pending(s.now()) {
			return errNotPending
		}
		to, err = fn(tx, g)
		return err
	})
	return to, err
}

// approve: with a passkey confirmation (so never by an agent).
func (s *Service) approve(w http.ResponseWriter, r *http.Request) error {
	p, err := s.Auth.RequireStepUp(r)
	if err != nil {
		return err
	}
	to, err := s.decide(r, func(tx *store.Store, g store.OAuthGrant) (string, error) {
		code, err := randomID(32)
		if err != nil {
			return "", err
		}
		if err := tx.ApproveOAuthGrant(r.Context(), g.ID, p.User.ID, hash(code), s.now().Add(CodeLife)); err != nil {
			return "", err
		}
		if err := tx.Audit(r.Context(), auth.AuditEntry(r, "oauth.approved", "oauth_client", g.ClientID, nil), nil,
			map[string]any{"client_name": g.ClientName, "redirect_uri": g.RedirectURI}); err != nil {
			return "", err
		}
		u, _ := url.Parse(g.RedirectURI)
		q := u.Query()
		q.Set("code", code)
		q.Set("iss", s.APIURL)
		if g.State != "" {
			q.Set("state", g.State)
		}
		u.RawQuery = q.Encode()
		return u.String(), nil
	})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"redirect_to": to})
	return nil
}

func (s *Service) deny(w http.ResponseWriter, r *http.Request) error {
	p, err := auth.Require(r)
	if err != nil {
		return err
	}
	to, err := s.decide(r, func(tx *store.Store, g store.OAuthGrant) (string, error) {
		if err := tx.DenyOAuthGrant(r.Context(), g.ID, p.User.ID); err != nil {
			return "", err
		}
		u, _ := url.Parse(g.RedirectURI)
		q := u.Query()
		q.Set("error", "access_denied")
		q.Set("error_description", "the person declined")
		q.Set("iss", s.APIURL)
		if g.State != "" {
			q.Set("state", g.State)
		}
		u.RawQuery = q.Encode()
		return u.String(), nil
	})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"redirect_to": to})
	return nil
}

// --- token ---

// pkce: base64url(SHA-256(verifier)) == challenge, in constant time.
func pkce(verifier, challenge string) bool {
	if l := len(verifier); l < 43 || l > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(want), []byte(challenge)) == 1
}

func (s *Service) token(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "token", 60, time.Minute) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "send a form-encoded body")
		return
	}
	f := r.PostForm
	if f.Get("grant_type") != "authorization_code" {
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "only authorization_code is supported")
		return
	}
	if res := f.Get("resource"); res != "" && strings.TrimSuffix(res, "/") != s.Resource() {
		oauthError(w, http.StatusBadRequest, "invalid_target", "this server only issues tokens for "+s.Resource())
		return
	}
	ctx := r.Context()
	var out map[string]any
	var invalid string
	err := s.Store.InTx(ctx, func(tx *store.Store) error {
		g, err := tx.LockOAuthGrantByCode(ctx, hash(f.Get("code")))
		if errors.Is(err, store.ErrNotFound) {
			invalid = "the code is unknown or was already used"
			return nil
		}
		if err != nil {
			return err
		}
		switch {
		case g.ApprovedAt == nil || g.UserID == nil || g.CodeExpiresAt == nil || !s.now().Before(*g.CodeExpiresAt):
			invalid = "the code has expired; connect again"
		case g.ClientID != f.Get("client_id"):
			invalid = "the code was issued to another client"
		case g.RedirectURI != f.Get("redirect_uri"):
			invalid = "redirect_uri doesn't match the one the code was issued for"
		case !pkce(f.Get("code_verifier"), g.CodeChallenge):
			invalid = "code_verifier doesn't match the code_challenge"
		}
		if invalid != "" {
			return nil
		}
		u, err := tx.UserByID(ctx, *g.UserID)
		if err != nil {
			return err
		}
		if u.FrozenAt != nil {
			invalid = "the person who approved this can't sign in any more"
			return nil
		}
		audit := store.AuditEntry{ActorID: &u.ID, ActorEmail: u.Email, IP: httpx.ClientIP(r), UserAgent: r.UserAgent()}
		t, secret, err := s.Agents.Mint(ctx, tx, u.ID, g.ClientName, TokenDays, &g.ClientID, audit)
		if err != nil {
			return err
		}
		if err := tx.ExchangeOAuthGrant(ctx, g.ID, t.ID); err != nil {
			return err
		}
		out = map[string]any{"access_token": secret, "token_type": "Bearer",
			"expires_in": int(t.ExpiresAt.Sub(s.now()).Seconds())}
		return nil
	})
	switch {
	case err != nil:
		s.fail(w, r, err)
	case invalid != "":
		oauthError(w, http.StatusBadRequest, "invalid_grant", invalid)
	default:
		writeJSON(w, http.StatusOK, out)
	}
}

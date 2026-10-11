package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/kmrifat/klavio-alternative/api/internal/config"
)

// oauthClient plays an AI client (claude.ai) talking to the API: no
// cookies, and it reads redirects instead of following them.
type oauthClient struct {
	t    *testing.T
	base string
	http *http.Client
}

func (c *oauthClient) get(path string) (*http.Response, []byte) {
	c.t.Helper()
	res, err := c.http.Get(c.base + path)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, b
}

func (c *oauthClient) postJSON(path string, v any) (*http.Response, map[string]any) {
	c.t.Helper()
	raw, _ := json.Marshal(v)
	res, err := c.http.Post(c.base+path, "application/json", strings.NewReader(string(raw)))
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res, out
}

func (c *oauthClient) postForm(path string, f url.Values) (int, map[string]any) {
	c.t.Helper()
	res, err := c.http.PostForm(c.base+path, f)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestOAuthConnector(t *testing.T) {
	const api = "https://api.acme.test"
	const callback = "https://claude.ai/api/mcp/auth_callback"
	a, srv, _ := setupWith(t, func(c *config.Config) {
		c.PublicAPIURL, c.OAuthRedirectHosts = api, []string{"claude.ai", "claude.com"}
	})
	ctx := context.Background()
	owner := newBrowser(t, srv.URL)
	link, _ := a.Bootstrap(ctx, "owner@acme.test", "Olivia Owner")
	owner.must(200, owner.acceptInvite(link))
	claude := &oauthClient{t: t, base: srv.URL, http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}

	// Discovery: /mcp's 401 points at the resource metadata, which points at us.
	res, _ := claude.postJSON("/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	if res.StatusCode != 401 || !strings.Contains(res.Header.Get("WWW-Authenticate"), `resource_metadata="`+api+`/.well-known/oauth-protected-resource/mcp"`) {
		t.Fatalf("401: %d %q", res.StatusCode, res.Header.Get("WWW-Authenticate"))
	}
	_, body := claude.get("/.well-known/oauth-protected-resource/mcp")
	var prm map[string]any
	_ = json.Unmarshal(body, &prm)
	if prm["resource"] != api+"/mcp" || prm["authorization_servers"].([]any)[0] != api {
		t.Fatalf("resource metadata: %s", body)
	}
	_, body = claude.get("/.well-known/oauth-authorization-server")
	var asm map[string]any
	_ = json.Unmarshal(body, &asm)
	if asm["token_endpoint"] != api+"/oauth/token" || asm["code_challenge_methods_supported"].([]any)[0] != "S256" {
		t.Fatalf("server metadata: %s", body)
	}

	// Registration: known clients' sites (claude.ai) or localhost only, so a
	// lookalike "Claude" can't collect a token from someone sent its link.
	for _, bad := range []string{"http://evil.example/cb", "https://evil.example/cb", "https://claude.ai.evil.example/cb", "http://claude.ai/cb", "https://сlaude.ai/cb"} {
		if res, out := claude.postJSON("/oauth/register", map[string]any{"client_name": "Claude", "redirect_uris": []string{bad}}); res.StatusCode != 400 || out["error"] != "invalid_redirect_uri" {
			t.Fatalf("%s registered: %d %v", bad, res.StatusCode, out)
		}
	}
	if res, _ := claude.postJSON("/oauth/register", map[string]any{"client_name": "Claude Code", "redirect_uris": []string{"http://localhost:33418/callback"}}); res.StatusCode != 201 {
		t.Fatalf("loopback refused: %d", res.StatusCode)
	}
	res, reg := claude.postJSON("/oauth/register", map[string]any{"client_name": "Claude", "redirect_uris": []string{callback}})
	if res.StatusCode != 201 {
		t.Fatalf("register: %d %v", res.StatusCode, reg)
	}
	clientID := reg["client_id"].(string)

	verifier := strings.Repeat("v3rifier-", 6)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	authorize := func(extra url.Values) *http.Response {
		q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {callback}, "state": {"xyz"},
			"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "resource": {api + "/mcp"}}
		for k, v := range extra {
			q[k] = v
		}
		r, _ := claude.get("/oauth/authorize?" + q.Encode())
		return r
	}
	// Never redirected to an address the client didn't register; other mistakes go back to the client.
	if r := authorize(url.Values{"redirect_uri": {"https://evil.example/cb"}}); r.StatusCode != 400 {
		t.Fatalf("unregistered redirect: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if r := authorize(url.Values{"code_challenge_method": {"plain"}}); r.StatusCode != 302 || !strings.Contains(r.Header.Get("Location"), "error=invalid_request") {
		t.Fatalf("no S256: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	r := authorize(nil)
	consent := r.Header.Get("Location")
	if r.StatusCode != 302 || !strings.HasPrefix(consent, origin+"/connect/") {
		t.Fatalf("authorize: %d %s", r.StatusCode, consent)
	}
	grant := strings.TrimPrefix(consent, origin+"/connect/")

	// The person sees who asks and where they'll go back to, and approves with a passkey.
	g := owner.must(200, owner.do("GET", "/admin/v1/oauth/grants/"+grant, nil))
	if g.body["client_name"] != "Claude" || g.body["redirect_host"] != "claude.ai" || g.body["status"] != "pending" {
		t.Fatalf("grant: %s", g.raw)
	}
	if _, err := a.Store.MarkSteppedUpForTest(ctx, "owner@acme.test"); err != nil {
		t.Fatal(err)
	}
	if r := owner.do("POST", "/admin/v1/oauth/grants/"+grant+"/approve", nil); r.errCode() != "step_up_required" {
		t.Fatalf("approve without passkey: %s", r.raw)
	}
	owner.must(200, owner.stepUp())
	to, _ := url.Parse(owner.must(200, owner.do("POST", "/admin/v1/oauth/grants/"+grant+"/approve", nil)).body["redirect_to"].(string))
	code := to.Query().Get("code")
	if to.Host != "claude.ai" || code == "" || to.Query().Get("state") != "xyz" || to.Query().Get("iss") != api {
		t.Fatalf("redirect_to: %s", to)
	}
	owner.must(409, owner.do("POST", "/admin/v1/oauth/grants/"+grant+"/approve", nil))

	// The code: PKCE-checked, for this client and redirect, once.
	form := func(verifier string) url.Values {
		return url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID},
			"redirect_uri": {callback}, "code_verifier": {verifier}, "resource": {api + "/mcp"}}
	}
	if st, out := claude.postForm("/oauth/token", form(strings.Repeat("x", 50))); st != 400 || out["error"] != "invalid_grant" {
		t.Fatalf("wrong verifier: %d %v", st, out)
	}
	st, tok := claude.postForm("/oauth/token", form(verifier))
	secret, _ := tok["access_token"].(string)
	if st != 200 || !strings.HasPrefix(secret, "kag_") || tok["token_type"] != "Bearer" || tok["expires_in"].(float64) < 89*24*3600 {
		t.Fatalf("token: %d %v", st, tok)
	}
	if st, out := claude.postForm("/oauth/token", form(verifier)); st != 400 || out["error"] != "invalid_grant" {
		t.Fatalf("code reused: %d %v", st, out)
	}

	// It works as an agent token for the person, and shows in Settings → AI agents.
	s, err := agentSession(t, srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}
	if text, failed := callTool(t, s, "whoami", nil); failed || !strings.Contains(text, "owner@acme.test") {
		t.Fatalf("whoami: %s", text)
	}
	ts := owner.must(200, owner.do("GET", "/admin/v1/agent-tokens", nil)).body["tokens"].([]any)
	if len(ts) != 1 || ts[0].(map[string]any)["name"] != "Claude" || ts[0].(map[string]any)["oauth_client_id"] != clientID {
		t.Fatalf("tokens: %v", ts)
	}

	// Denied: the client hears access_denied; an expired code is refused.
	grant2 := strings.TrimPrefix(authorize(nil).Header.Get("Location"), origin+"/connect/")
	denied, _ := url.Parse(owner.must(200, owner.do("POST", "/admin/v1/oauth/grants/"+grant2+"/deny", nil)).body["redirect_to"].(string))
	if denied.Query().Get("error") != "access_denied" || denied.Query().Get("state") != "xyz" {
		t.Fatalf("deny: %s", denied)
	}
	grant3 := strings.TrimPrefix(authorize(nil).Header.Get("Location"), origin+"/connect/")
	to3, _ := url.Parse(owner.must(200, owner.do("POST", "/admin/v1/oauth/grants/"+grant3+"/approve", nil)).body["redirect_to"].(string))
	if err := a.Store.ExecForTest(ctx, `UPDATE oauth_grants SET code_expires_at = now() - interval '1 second' WHERE id = $1`, grant3); err != nil {
		t.Fatal(err)
	}
	code = to3.Query().Get("code")
	if st, out := claude.postForm("/oauth/token", form(verifier)); st != 400 || !strings.Contains(out["error_description"].(string), "expired") {
		t.Fatalf("expired code: %d %v", st, out)
	}
}

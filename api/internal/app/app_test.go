package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/app"
	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/blocklist"
	"github.com/kmrifat/klavio-alternative/api/internal/config"
	"github.com/kmrifat/klavio-alternative/api/internal/ses"
	"github.com/kmrifat/klavio-alternative/api/internal/testdb"
	"github.com/kmrifat/klavio-alternative/api/internal/webauthntest"
)

const origin = "http://localhost:5173"

// --- fakes ---

type upPinger struct{}

func (upPinger) Ping(context.Context) error { return nil }

type fakeDNS struct {
	mu    sync.Mutex
	cname map[string]string
	mx    map[string][]*net.MX
	txt   map[string][]string
	hosts map[string]bool // domains with an A record but no MX
	// answers: A records by name (blocklist zones answer 127.0.x.y when listed)
	answers map[string][]string
	// blocklistQueries counts fully qualified lookups (only blocklists use them).
	blocklistQueries int
	hang             bool // MX lookups wait until the context ends (a stuck DNS server)
}

func nx(name string) error { return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true} }

func (f *fakeDNS) LookupCNAME(_ context.Context, h string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.cname[h]; ok {
		return v, nil
	}
	return "", nx(h)
}

func (f *fakeDNS) LookupMX(ctx context.Context, n string) ([]*net.MX, error) {
	f.mu.Lock()
	if f.hang {
		f.mu.Unlock()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	defer f.mu.Unlock()
	if v, ok := f.mx[n]; ok {
		return v, nil
	}
	return nil, nx(n)
}

func (f *fakeDNS) LookupTXT(_ context.Context, n string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.txt[n]; ok {
		return v, nil
	}
	return nil, nx(n)
}

// publish puts a brand's required records into DNS, as the brand's admin would.
func (f *fakeDNS) LookupHost(_ context.Context, h string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasSuffix(h, ".") {
		f.blocklistQueries++
	}
	if v, ok := f.answers[h]; ok {
		return v, nil
	}
	if _, ok := f.mx[h]; ok {
		return []string{"192.0.2.1"}, nil
	}
	if f.hosts[h] {
		return []string{"192.0.2.1"}, nil
	}
	return nil, nx(h)
}

func (f *fakeDNS) publish(records []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range records {
		name, value := r["name"].(string), r["value"].(string)
		switch r["type"] {
		case "CNAME":
			f.cname[name] = value + "."
		case "MX":
			_, host, _ := strings.Cut(value, " ")
			f.mx[name] = []*net.MX{{Host: host + ".", Pref: 10}}
		case "TXT":
			f.txt[name] = append(f.txt[name], value)
		}
	}
}

// --- HTTP client acting as one browser ---

type browser struct {
	t       *testing.T
	base    string
	http    *http.Client
	key     *webauthntest.Authenticator
	country string // sent as CF-IPCountry, like Cloudflare does
}

func newBrowser(t *testing.T, base string) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, base: base, http: &http.Client{Jar: jar}, key: &webauthntest.Authenticator{Origin: origin}}
}

type resp struct {
	status int
	body   map[string]any
	raw    []byte
}

func (r resp) errCode() string {
	e, _ := r.body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func (b *browser) do(method, path string, body any) resp {
	b.t.Helper()
	var rd io.Reader
	switch v := body.(type) {
	case nil:
		if method != http.MethodGet {
			rd = strings.NewReader("{}")
		}
	case []byte:
		rd = bytes.NewReader(v)
	default:
		raw, _ := json.Marshal(v)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, b.base+path, rd)
	req.Header.Set("Origin", origin)
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if b.country != "" {
		req.Header.Set("CF-IPCountry", b.country)
	}
	res, err := b.http.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, raw: raw}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func (b *browser) must(want int, r resp) resp {
	b.t.Helper()
	if r.status != want {
		b.t.Fatalf("status %d, want %d: %s", r.status, want, r.raw)
	}
	return r
}

// ceremony runs begin → authenticator → finish.
func (b *browser) ceremony(beginPath, finishPath string, create bool) resp {
	b.t.Helper()
	begin := b.must(200, b.do("POST", beginPath, nil))
	opts, _ := json.Marshal(begin.body["options"])
	var cred []byte
	var err error
	if create {
		cred, err = b.key.Create(opts)
	} else {
		cred, err = b.key.Get(opts)
	}
	if err != nil {
		b.t.Fatal(err)
	}
	return b.do("POST", finishPath+"?ceremony="+begin.body["ceremony"].(string), cred)
}

func (b *browser) acceptInvite(link string) resp {
	b.t.Helper()
	token := link[strings.LastIndex(link, "/")+1:]
	b.must(200, b.do("GET", "/admin/v1/invites/"+token, nil))
	return b.ceremony("/admin/v1/invites/"+token+"/begin", "/admin/v1/invites/"+token+"/finish", true)
}

func (b *browser) signIn() resp {
	return b.ceremony("/admin/v1/auth/login/begin", "/admin/v1/auth/login/finish", false)
}

func (b *browser) stepUp() resp {
	return b.ceremony("/admin/v1/auth/stepup/begin", "/admin/v1/auth/stepup/finish", false)
}

// --- the test ---

// inlineRunner runs import jobs inside the request, so tests needn't wait for a worker.
type inlineRunner struct{ a **app.App }

func (r inlineRunner) StartCheck(ctx context.Context, brandID, id uuid.UUID) error {
	return (*r.a).Imports.RunCheck(context.WithoutCancel(ctx), brandID, id)
}

func (r inlineRunner) StartCommit(ctx context.Context, brandID, id uuid.UUID) error {
	return (*r.a).Imports.RunCommit(context.WithoutCancel(ctx), brandID, id)
}

// QueueRebuild recompiles templates right away, after the change committed.
func (r inlineRunner) QueueRebuild(ctx context.Context, brandID uuid.UUID) error {
	return (*r.a).Templates.RebuildBrand(context.WithoutCancel(ctx), brandID)
}

// setup builds the app with the test client acting as cloudflared on
// localhost: its CF-* headers are trusted, as Cloudflare's would be.
func setup(t *testing.T) (*app.App, *httptest.Server, *fakeDNS) {
	return setupWith(t, nil)
}

func setupWith(t *testing.T, edit func(*config.Config)) (*app.App, *httptest.Server, *fakeDNS) {
	pool := testdb.New(t)
	var appRef *app.App
	dns := &fakeDNS{cname: map[string]string{}, mx: map[string][]*net.MX{}, txt: map[string][]string{}, hosts: map[string]bool{}, answers: map[string][]string{}}
	cfg := config.Config{
		Env: config.EnvDevelopment, AllowedOrigins: []string{origin}, AppURL: origin,
		WebAuthnRPID: "localhost", WebAuthnRPName: "Test",
		UnsubscribeSecret:      "test-unsubscribe-secret-0123456789abcdef",
		TrustCloudflareHeaders: true,
		TrustedProxies:         []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")},
	}
	if edit != nil {
		edit(&cfg)
	}
	a, err := app.New(app.Deps{
		Config:            cfg,
		Log:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		Pool:              pool,
		Redis:             upPinger{},
		Ceremonies:        &auth.MemoryCeremonies{},
		SES:               &ses.Fake{},
		DNS:               dns,
		Version:           "test",
		ImportRunner:      inlineRunner{a: &appRef},
		TemplateRebuilder: inlineRunner{a: &appRef},
		Blocklists:        &blocklist.Checker{R: dns},
	})
	if err != nil {
		t.Fatal(err)
	}
	appRef = a
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return a, srv, dns
}

var brightside = map[string]any{
	"name": "Brightside", "slug": "brightside", "color": "#F97316",
	"postal_address": "221B Market St, San Francisco", "sending_domain": "news.brightside.co", "link_domain": "go.brightside.co",
}

func TestOwnerFlow(t *testing.T) {
	a, srv, dns := setup(t)
	ctx := context.Background()
	owner := newBrowser(t, srv.URL)

	owner.must(401, owner.do("GET", "/admin/v1/me", nil))

	// Bootstrap → accept invite with a new passkey. Re-running bootstrap
	// before anyone joined replaces the earlier link.
	lost, err := a.Bootstrap(ctx, "Owner@Acme.test", "Olivia Owner")
	if err != nil {
		t.Fatal(err)
	}
	link, err := a.Bootstrap(ctx, "Owner@Acme.test", "Olivia Owner")
	if err != nil {
		t.Fatal(err)
	}
	if r := owner.do("GET", "/admin/v1/invites/"+lost[strings.LastIndex(lost, "/")+1:], nil); r.status != 404 {
		t.Fatalf("replaced bootstrap link should be invalid, got %d", r.status)
	}
	me := owner.must(200, owner.acceptInvite(link))
	if _, err := a.Bootstrap(ctx, "second@acme.test", "Second"); err == nil {
		t.Fatal("bootstrap after the first owner joined should fail")
	}
	user := me.body["user"].(map[string]any)
	if user["email"] != "owner@acme.test" || user["company_role"] != "owner" || me.body["stepped_up_until"] == nil {
		t.Fatalf("unexpected me: %s", me.raw)
	}
	if r := newBrowser(t, srv.URL).do("GET", "/admin/v1/invites/"+link[strings.LastIndex(link, "/")+1:], nil); r.status != 404 {
		t.Fatalf("used invite should be gone, got %d", r.status)
	}

	// Brands: validation, creation, DNS records.
	apex := map[string]any{}
	for k, v := range brightside {
		apex[k] = v
	}
	apex["sending_domain"] = "brightside.co"
	if r := owner.do("POST", "/admin/v1/brands", apex); r.status != 400 || r.errCode() != "invalid_sending_domain" {
		t.Fatalf("apex domain should be rejected: %d %s", r.status, r.raw)
	}
	created := owner.must(201, owner.do("POST", "/admin/v1/brands", brightside))
	brand := created.body["brand"].(map[string]any)
	recs := created.body["dns_records"].([]any)
	if brand["status"] != "setup" || brand["color"] != "#f97316" || brand["mail_from_domain"] != "bounce.news.brightside.co" || len(recs) != 6 {
		t.Fatalf("unexpected brand: %s", created.raw)
	}
	if r := owner.do("POST", "/admin/v1/brands", brightside); r.status != 409 {
		t.Fatalf("duplicate brand: got %d", r.status)
	}

	// Nothing published yet → stays in setup; publish → ready (DMARC p=none is only a warning).
	checked := owner.must(200, owner.do("POST", "/admin/v1/brands/brightside/dns-check", nil))
	if checked.body["brand"].(map[string]any)["status"] != "setup" {
		t.Fatalf("expected setup: %s", checked.raw)
	}
	var toPublish []map[string]any
	for _, r := range recs {
		toPublish = append(toPublish, r.(map[string]any))
	}
	dns.publish(toPublish)
	checked = owner.must(200, owner.do("POST", "/admin/v1/brands/brightside/dns-check", nil))
	if checked.body["brand"].(map[string]any)["status"] != "ready" {
		t.Fatalf("expected ready: %s", checked.raw)
	}
	for _, r := range checked.body["dns_records"].([]any) {
		rec := r.(map[string]any)
		want := "ok"
		if rec["purpose"] == "dmarc" {
			want = "warning"
		}
		if rec["status"] != want {
			t.Errorf("%s %s: status %s, want %s (%s)", rec["purpose"], rec["name"], rec["status"], want, rec["detail"])
		}
	}

	// Sign out, sign back in with the same passkey.
	owner.must(204, owner.do("POST", "/admin/v1/auth/logout", nil))
	owner.must(401, owner.do("GET", "/admin/v1/me", nil))
	owner.must(200, owner.signIn())
	owner.must(200, owner.do("GET", "/admin/v1/me", nil))

	// A challenge can only be used once.
	begin := owner.must(200, owner.do("POST", "/admin/v1/auth/login/begin", nil))
	opts, _ := json.Marshal(begin.body["options"])
	cred, _ := owner.key.Get(opts)
	owner.must(200, owner.do("POST", "/admin/v1/auth/login/finish?ceremony="+begin.body["ceremony"].(string), cred))
	if r := owner.do("POST", "/admin/v1/auth/login/finish?ceremony="+begin.body["ceremony"].(string), cred); r.errCode() != "ceremony_expired" {
		t.Fatalf("replayed ceremony: %d %s", r.status, r.raw)
	}

	// Sensitive actions need a recent passkey confirmation.
	if _, err := a.Store.MarkSteppedUpForTest(ctx, "owner@acme.test"); err != nil {
		t.Fatal(err)
	}
	nimbus := map[string]any{
		"name": "Nimbus", "slug": "nimbus", "color": "#3b82f6", "postal_address": "1 Cloud Way",
		"sending_domain": "updates.nimbusnotes.app", "link_domain": "go.nimbusnotes.app",
	}
	if r := owner.do("POST", "/admin/v1/brands", nimbus); r.errCode() != "step_up_required" {
		t.Fatalf("expected step_up_required: %d %s", r.status, r.raw)
	}
	owner.must(200, owner.stepUp())
	owner.must(201, owner.do("POST", "/admin/v1/brands", nimbus))
}

func TestTeamRoles(t *testing.T) {
	a, srv, _ := setup(t)
	ctx := context.Background()

	owner := newBrowser(t, srv.URL)
	link, _ := a.Bootstrap(ctx, "owner@acme.test", "Olivia Owner")
	owner.must(200, owner.acceptInvite(link))
	b := owner.must(201, owner.do("POST", "/admin/v1/brands", brightside)).body["brand"].(map[string]any)
	brandID := b["id"].(string)

	// Owner invites an editor for Brightside.
	inv := owner.must(201, owner.do("POST", "/admin/v1/team/invites", map[string]any{
		"email": "jon@acme.test", "name": "Jon Park", "company_role": "member",
		"brand_roles": map[string]string{brandID: "editor"},
	}))
	if r := owner.do("POST", "/admin/v1/team/invites", map[string]any{"email": "jon@acme.test", "name": "Jon"}); r.status != 409 {
		t.Fatalf("duplicate invite: %d %s", r.status, r.raw)
	}
	editor := newBrowser(t, srv.URL)
	me := editor.must(200, editor.acceptInvite(inv.body["invite_url"].(string)))
	if me.body["brand_roles"].(map[string]any)[brandID] != "editor" {
		t.Fatalf("editor role missing: %s", me.raw)
	}
	editorID := me.body["user"].(map[string]any)["id"].(string)

	// Editor: can see the brand, can't create brands, edit the brand, invite or read the audit log.
	list := editor.must(200, editor.do("GET", "/admin/v1/brands", nil)).body["brands"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["my_role"] != "editor" {
		t.Fatalf("editor brand list: %v", list)
	}
	editor.must(403, editor.do("POST", "/admin/v1/brands", map[string]any{"name": "x"}))
	editor.must(403, editor.do("PATCH", "/admin/v1/brands/brightside", map[string]any{"name": "Hacked"}))
	editor.must(403, editor.do("POST", "/admin/v1/team/invites", map[string]any{"email": "x@acme.test", "name": "X"}))
	editor.must(403, editor.do("GET", "/admin/v1/audit", nil))
	editor.must(403, editor.do("PUT", "/admin/v1/team/members/"+editorID+"/roles", map[string]any{"company_role": "owner"}))

	// Owner promotes them to brand admin → they can edit the brand.
	owner.must(204, owner.do("PUT", "/admin/v1/team/members/"+editorID+"/roles", map[string]any{
		"company_role": "member", "brand_roles": map[string]string{brandID: "brand_admin"},
	}))
	edited := editor.must(200, editor.do("PATCH", "/admin/v1/brands/brightside", map[string]any{"name": "Brightside Co"}))
	if edited.body["brand"].(map[string]any)["name"] != "Brightside Co" {
		t.Fatalf("edit failed: %s", edited.raw)
	}

	// Guard rails on roles.
	ownerID := owner.must(200, owner.do("GET", "/admin/v1/me", nil)).body["user"].(map[string]any)["id"].(string)
	if r := owner.do("PUT", "/admin/v1/team/members/"+ownerID+"/roles", map[string]any{"company_role": "member"}); r.errCode() != "own_role" {
		t.Fatalf("self-demotion: %d %s", r.status, r.raw)
	}
	if r := owner.do("PUT", "/admin/v1/team/members/"+editorID+"/roles", map[string]any{
		"company_role": "member", "brand_roles": map[string]string{"00000000-0000-0000-0000-000000000000": "viewer"},
	}); r.errCode() != "unknown_brand" {
		t.Fatalf("unknown brand: %d %s", r.status, r.raw)
	}

	// Unknown brands answer 404, not 403.
	editor.must(404, editor.do("GET", "/admin/v1/brands/does-not-exist", nil))

	// Team list and audit log reflect everything above.
	team := owner.must(200, owner.do("GET", "/admin/v1/team", nil))
	if n := len(team.body["members"].([]any)); n != 2 {
		t.Fatalf("members = %d", n)
	}
	entries := owner.must(200, owner.do("GET", "/admin/v1/audit", nil)).body["entries"].([]any)
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.(map[string]any)["action"].(string)] = true
	}
	for _, want := range []string{"user.joined", "brand.created", "user.invited", "user.roles_changed", "brand.updated"} {
		if !seen[want] {
			t.Errorf("audit log missing %s (have %v)", want, seen)
		}
	}

	// The audit log can't be edited, even by the app's own database user.
	if _, err := a.Store.TryEditAuditForTest(ctx); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("audit log edit should fail, got %v", err)
	}
}

func TestCrossSiteWritesBlocked(t *testing.T) {
	_, srv, _ := setup(t)
	req, _ := http.NewRequest("POST", srv.URL+"/admin/v1/auth/login/begin", strings.NewReader("{}"))
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", res.StatusCode)
	}
}

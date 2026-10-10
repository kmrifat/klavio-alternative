package app_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/app"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// apiCall is a server-to-server Contacts API request.
func apiCall(t *testing.T, base, method, path, key string, body any, headers ...string) resp {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, raw: raw}
	_ = json.Unmarshal(raw, &out.body)
	if res.Header.Get("Idempotent-Replayed") == "true" {
		out.body["_replayed"] = true
	}
	return out
}

var linkRe = regexp.MustCompile(`/confirm/[A-Za-z0-9_-]+`)

// confirmLink pulls the confirmation link's path for `to` out of the outbox
// (the email has https://go.<brand>/…; the test server stands in for it).
func confirmLink(t *testing.T, a *app.App, to, kind string) string {
	t.Helper()
	for _, m := range outbox(a) {
		if m.To == to && m.Kind == kind {
			if l := linkRe.FindString(m.Text); l != "" {
				return l
			}
		}
	}
	t.Fatalf("no %s email to %s in %d messages", kind, to, len(outbox(a)))
	return ""
}

func sentTo(a *app.App, to, kind string) int {
	n := 0
	for _, m := range outbox(a) {
		if m.To == to && m.Kind == kind {
			n++
		}
	}
	return n
}

type apiFixture struct {
	a       *app.App
	srv     string
	owner   *browser
	brandID uuid.UUID
	wording string
}

func newAPIFixture(t *testing.T) apiFixture {
	t.Helper()
	a, srv, dns := setup(t)
	owner := newBrowser(t, srv.URL)
	link, _ := a.Bootstrap(context.Background(), "owner@acme.test", "Olivia Owner")
	owner.must(200, owner.acceptInvite(link))
	created := owner.must(201, owner.do("POST", "/admin/v1/brands", brightside))
	var recs []map[string]any
	for _, r := range created.body["dns_records"].([]any) {
		recs = append(recs, r.(map[string]any))
	}
	dns.publish(recs)
	owner.must(200, owner.do("POST", "/admin/v1/brands/brightside/dns-check", nil))
	w := owner.must(201, owner.do("POST", "/admin/v1/brands/brightside/consent-wordings", map[string]any{
		"label": "Footer form", "text": "Yes, send me Brightside news and offers. I can unsubscribe any time.",
	}))
	return apiFixture{a: a, srv: srv.URL, owner: owner, wording: w.body["id"].(string),
		brandID: uuid.MustParse(created.body["brand"].(map[string]any)["id"].(string))}
}

func (f apiFixture) key(t *testing.T, scopes []string, allow ...string) (string, resp) {
	t.Helper()
	body := map[string]any{"name": "Website", "scopes": scopes}
	if len(allow) > 0 {
		body["ip_allowlist"] = allow
	}
	r := f.owner.must(201, f.owner.do("POST", "/admin/v1/brands/brightside/api-keys", body))
	return r.body["secret"].(string), r
}

func (f apiFixture) signup(email string) map[string]any {
	return map[string]any{"email": email, "first_name": "Ann", "consent": map[string]any{
		"wording_id": f.wording, "page_url": "https://brightside.co/signup", "ip": "203.0.113.9"}}
}

func TestContactsAPIDoubleOptIn(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()

	// Wordings are immutable: the database refuses edits.
	if err := f.a.Store.TryEditWordingForTest(ctx, uuid.MustParse(f.wording)); err == nil {
		t.Fatal("consent wording text was edited")
	}
	for _, q := range []string{
		`TRUNCATE consent_wordings CASCADE`,
		`DELETE FROM consent_wordings`,
		`UPDATE consent_wordings SET created_by = NULL`,
		`UPDATE consent_wordings SET id = gen_random_uuid()`,
	} {
		if err := f.a.Store.ExecForTest(ctx, q); err == nil {
			t.Fatalf("%s was allowed", q)
		}
	}
	f.owner.must(400, f.owner.do("POST", "/admin/v1/brands/brightside/consent-wordings", map[string]any{"label": "x", "text": "short"}))

	// One admin in the company: the key works at once. The secret is only in this response.
	secret, created := f.key(t, []string{"contacts:write", "contacts:read"})
	if created.body["key"].(map[string]any)["state"] != "active" || !strings.HasPrefix(secret, "kla_") {
		t.Fatalf("key: %s", created.raw)
	}
	list := f.owner.must(200, f.owner.do("GET", "/admin/v1/brands/brightside/api-keys", nil))
	if strings.Contains(string(list.raw), secret) || strings.Contains(string(list.raw), "key_hash") {
		t.Fatalf("key list leaks the secret: %s", list.raw)
	}

	// Sign-up → pending + confirmation email.
	r := apiCall(t, f.srv, "POST", "/api/v1/contacts", secret, f.signup("Ann@Example.com"))
	if r.status != 202 || r.body["status"] != "pending_confirmation" || r.body["email"] != "ann@example.com" {
		t.Fatalf("subscribe: %d %s", r.status, r.raw)
	}
	link := confirmLink(t, f.a, "ann@example.com", "confirmation")
	ann, err := f.a.Store.ContactByEmail(ctx, f.brandID, "ann@example.com")
	if err != nil || ann.Status != store.ContactPending {
		t.Fatalf("before confirming: %+v %v", ann, err)
	}

	// The link's GET only shows a button (scanners can't confirm); POST confirms.
	page := newBrowser(t, f.srv)
	if g := page.do("GET", link, nil); g.status != 200 || !strings.Contains(string(g.raw), "Yes, subscribe me") {
		t.Fatalf("confirm page: %d %s", g.status, g.raw)
	}
	if ann, _ = f.a.Store.ContactByEmail(ctx, f.brandID, "ann@example.com"); ann.Status != store.ContactPending {
		t.Fatal("GET confirmed the contact")
	}
	if p := page.do("POST", link, nil); p.status != 200 || !strings.Contains(string(p.raw), "You're subscribed") {
		t.Fatalf("confirm: %d %s", p.status, p.raw)
	}
	if p := page.do("POST", link, nil); !strings.Contains(string(p.raw), "Already confirmed") {
		t.Fatalf("second click: %s", p.raw)
	}
	if g := page.do("GET", link[:len(link)-2]+"xx", nil); g.status != 404 {
		t.Fatalf("tampered link: %d", g.status)
	}
	ann, _ = f.a.Store.ContactByEmail(ctx, f.brandID, "ann@example.com")
	if ann.Status != store.ContactSubscribed || ann.ConsentBasis != "double_opt_in" || ann.ConsentedAt == nil || ann.TrackingAllowed {
		t.Fatalf("after confirming: %+v", ann)
	}
	hist, _ := f.a.Store.ConsentHistory(ctx, ann.ID)
	if len(hist) != 2 || hist[0].Kind != "confirmed" || hist[1].Kind != "opt_in" || hist[1].Method != "api" ||
		!strings.Contains(hist[1].Wording, "Brightside news") || hist[1].IP != "203.0.113.9" || hist[1].PageURL != "https://brightside.co/signup" {
		t.Fatalf("consent history: %+v", hist)
	}

	// Already subscribed: 200, no new email.
	before := sentTo(f.a, "ann@example.com", "confirmation")
	if r := apiCall(t, f.srv, "POST", "/api/v1/contacts", secret, f.signup("ann@example.com")); r.status != 200 || r.body["status"] != "subscribed" {
		t.Fatalf("resubmit: %d %s", r.status, r.raw)
	}
	if sentTo(f.a, "ann@example.com", "confirmation") != before {
		t.Fatal("subscribed contact got another confirmation")
	}

	// New topics for a subscriber wait for their confirmation; omitting topics changes nothing.
	deals := f.owner.must(201, f.owner.do("POST", "/admin/v1/brands/brightside/topics", map[string]any{"name": "Deals"})).body["id"].(string)
	topicsOf := func() int {
		ids, err := f.a.Store.ContactTopicIDs(ctx, ann.ID)
		if err != nil {
			t.Fatal(err)
		}
		return len(ids)
	}
	if n := topicsOf(); n != 1 {
		t.Fatalf("ann starts with %d topics", n)
	}
	latestLink := func() string {
		l := ""
		for _, m := range outbox(f.a) {
			if m.To == "ann@example.com" {
				l = linkRe.FindString(m.Text)
			}
		}
		return l
	}
	request := func(topic string) string {
		t.Helper()
		body := f.signup("ann@example.com")
		body["topics"] = []string{topic}
		r := apiCall(t, f.srv, "POST", "/api/v1/contacts", secret, body)
		if r.status != 202 || r.body["status"] != "subscribed" || len(r.body["pending_topics"].([]any)) != 1 || r.body["confirmation_email"] != "sent" {
			t.Fatalf("new topic: %d %s", r.status, r.raw)
		}
		return latestLink()
	}
	dealsLink := request(deals)
	events := f.owner.must(201, f.owner.do("POST", "/admin/v1/brands/brightside/topics", map[string]any{"name": "Events"})).body["id"].(string)
	eventsLink := request(events)
	if n := topicsOf(); n != 1 {
		t.Fatal("topic added before confirming")
	}
	if r := apiCall(t, f.srv, "POST", "/api/v1/contacts", secret, f.signup("ann@example.com")); r.status != 200 || r.body["pending_topics"] != nil {
		t.Fatalf("no topics given: %d %s", r.status, r.raw)
	}
	// The original sign-up link confirms the sign-up only, not later requests.
	if p := page.do("POST", link, nil); !strings.Contains(string(p.raw), "Already confirmed") || topicsOf() != 1 {
		t.Fatalf("old sign-up link applied topic requests: %s (%d topics)", p.raw, topicsOf())
	}
	// Each request's link shows and applies exactly its topics.
	g := page.do("GET", dealsLink, nil)
	if !strings.Contains(string(g.raw), "<li>Deals</li>") || strings.Contains(string(g.raw), "Events") {
		t.Fatalf("deals page should list Deals only: %s", g.raw)
	}
	if p := page.do("POST", dealsLink, nil); !strings.Contains(string(p.raw), "You're subscribed") {
		t.Fatalf("deals confirm: %s", p.raw)
	}
	ids, _ := f.a.Store.ContactTopicIDs(ctx, ann.ID)
	if len(ids) != 2 || !slices.Contains(ids, uuid.MustParse(deals)) || slices.Contains(ids, uuid.MustParse(events)) {
		t.Fatalf("after the deals link: %v", ids)
	}
	if p := page.do("POST", dealsLink, nil); !strings.Contains(string(p.raw), "Already confirmed") {
		t.Fatalf("deals link twice: %s", p.raw)
	}
	if p := page.do("POST", eventsLink, nil); !strings.Contains(string(p.raw), "You're subscribed") || topicsOf() != 3 {
		t.Fatalf("events confirm: %s (%d topics)", p.raw, topicsOf())
	}
	hist, _ = f.a.Store.ConsentHistory(ctx, ann.ID)
	if hist[0].Kind != "confirmed" || !strings.Contains(hist[0].Source, "Events") {
		t.Fatalf("topic consent record: %+v", hist[0])
	}

	// Lookup and unsubscribe.
	if r := apiCall(t, f.srv, "GET", "/api/v1/contacts/ann@example.com", secret, nil); r.status != 200 || r.body["status"] != "subscribed" {
		t.Fatalf("lookup: %d %s", r.status, r.raw)
	}
	if r := apiCall(t, f.srv, "POST", "/api/v1/contacts/ann@example.com/unsubscribe", secret, map[string]any{}); r.status != 200 {
		t.Fatalf("unsubscribe: %d %s", r.status, r.raw)
	}
	// Signing up again after unsubscribing means confirming again.
	if r := apiCall(t, f.srv, "POST", "/api/v1/contacts", secret, f.signup("ann@example.com")); r.status != 202 {
		t.Fatalf("re-signup: %d %s", r.status, r.raw)
	}
	if ann, _ = f.a.Store.ContactByEmail(ctx, f.brandID, "ann@example.com"); ann.Status != store.ContactPending {
		t.Fatalf("re-signup status: %s", ann.Status)
	}

	// A few confirmation emails a day per address, however often the API is called.
	var outcomes []any
	for range 5 {
		outcomes = append(outcomes, apiCall(t, f.srv, "POST", "/api/v1/contacts", secret, f.signup("bob@example.com")).body["confirmation_email"])
	}
	if n := sentTo(f.a, "bob@example.com", "confirmation"); n != 3 || outcomes[2] != "sent" || outcomes[3] != "limit_reached" {
		t.Fatalf("%d confirmation emails to one address: %v", n, outcomes)
	}

	// Validation.
	for name, body := range map[string]map[string]any{
		"invalid_email":    f.signup("not-an-email"),
		"disposable_email": f.signup("x@mailinator.com"),
		"unknown_wording":  {"email": "c@example.com", "consent": map[string]any{"wording_id": uuid.NewString()}},
		"consent_required": {"email": "c@example.com"},
		"unknown_topic":    {"email": "c@example.com", "topics": []string{uuid.NewString()}, "consent": map[string]any{"wording_id": f.wording}},
	} {
		if r := apiCall(t, f.srv, "POST", "/api/v1/contacts", secret, body); r.status != 422 || r.errCode() != name {
			t.Errorf("%s: %d %s", name, r.status, r.raw)
		}
	}
	// A retired wording can't be used for new sign-ups.
	f.owner.must(200, f.owner.do("POST", "/admin/v1/brands/brightside/consent-wordings/"+f.wording+"/retire", nil))
	if r := apiCall(t, f.srv, "POST", "/api/v1/contacts", secret, f.signup("dee@example.com")); r.errCode() != "retired_wording" {
		t.Fatalf("retired wording: %d %s", r.status, r.raw)
	}

	// The call log masks addresses.
	calls := f.owner.must(200, f.owner.do("GET", "/admin/v1/brands/brightside/api-calls", nil))
	if strings.Contains(string(calls.raw), "ann@example.com") || !strings.Contains(string(calls.raw), "a•••@example.com") {
		t.Fatalf("call log: %s", calls.raw)
	}
}

func TestContactsAPIKeys(t *testing.T) {
	f := newAPIFixture(t)
	body := f.signup("ann@example.com")

	// Authentication.
	if r := apiCall(t, f.srv, "POST", "/api/v1/contacts", "", body); r.status != 401 || r.errCode() != "missing_key" {
		t.Fatalf("no key: %d %s", r.status, r.raw)
	}
	if r := apiCall(t, f.srv, "POST", "/api/v1/contacts", "kla_nope_nope", body); r.status != 401 || r.errCode() != "invalid_key" {
		t.Fatalf("bad key: %d %s", r.status, r.raw)
	}
	readOnly, _ := f.key(t, []string{"contacts:read"})
	if r := apiCall(t, f.srv, "POST", "/api/v1/contacts", readOnly, body); r.status != 403 || r.errCode() != "missing_scope" {
		t.Fatalf("read-only key: %d %s", r.status, r.raw)
	}

	// IP allowlist: the client IP comes from Cloudflare's header via the trusted proxy.
	ipKey, _ := f.key(t, []string{"contacts:write"}, "198.51.100.0/24")
	if r := apiCall(t, f.srv, "POST", "/api/v1/contacts", ipKey, body, "CF-Connecting-IP", "203.0.113.50"); r.errCode() != "ip_not_allowed" {
		t.Fatalf("outside allowlist: %d %s", r.status, r.raw)
	}
	if r := apiCall(t, f.srv, "POST", "/api/v1/contacts", ipKey, body, "CF-Connecting-IP", "198.51.100.7"); r.status != 202 {
		t.Fatalf("inside allowlist: %d %s", r.status, r.raw)
	}
	f.owner.must(400, f.owner.do("POST", "/admin/v1/brands/brightside/api-keys", map[string]any{"name": "x", "scopes": []string{"contacts:write"}, "ip_allowlist": []string{"0.0.0.0/0"}}))
	f.owner.must(400, f.owner.do("POST", "/admin/v1/brands/brightside/api-keys", map[string]any{"name": "x", "scopes": []string{"contacts:admin"}}))
	f.owner.must(400, f.owner.do("POST", "/admin/v1/brands/brightside/api-keys", map[string]any{"name": "x", "scopes": []string{"contacts:write"}, "expires_in_days": 91}))

	// Idempotency: same key + body → same answer; different body → refused.
	key, created := f.key(t, []string{"contacts:write"})
	first := apiCall(t, f.srv, "POST", "/api/v1/contacts", key, f.signup("eve@example.com"), "Idempotency-Key", "k-1")
	again := apiCall(t, f.srv, "POST", "/api/v1/contacts", key, f.signup("eve@example.com"), "Idempotency-Key", "k-1")
	if first.status != 202 || again.status != 202 || again.body["_replayed"] != true {
		t.Fatalf("replay: %d %s / %d %s", first.status, first.raw, again.status, again.raw)
	}
	if n := sentTo(f.a, "eve@example.com", "confirmation"); n != 1 {
		t.Fatalf("replay sent %d emails", n)
	}
	if r := apiCall(t, f.srv, "POST", "/api/v1/contacts", key, f.signup("zed@example.com"), "Idempotency-Key", "k-1"); r.status != 422 || r.errCode() != "idempotency_mismatch" {
		t.Fatalf("reused key: %d %s", r.status, r.raw)
	}

	// A claim left behind by a crashed request blocks the key for 2 minutes, not a day.
	stuck := f.signup("sam@example.com")
	raw, _ := json.Marshal(stuck)
	h := sha256.New()
	h.Write([]byte("POST /api/v1/contacts\x00"))
	h.Write(raw)
	keyID := uuid.MustParse(created.body["key"].(map[string]any)["id"].(string))
	if ok, _, err := f.a.Store.ClaimIdempotency(context.Background(), keyID, "k-stuck", h.Sum(nil)); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if r := apiCall(t, f.srv, "POST", "/api/v1/contacts", key, stuck, "Idempotency-Key", "k-stuck"); r.status != 409 || r.errCode() != "request_in_progress" {
		t.Fatalf("fresh claim: %d %s", r.status, r.raw)
	}
	if err := f.a.Store.AgeIdempotencyForTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := apiCall(t, f.srv, "POST", "/api/v1/contacts", key, stuck, "Idempotency-Key", "k-stuck"); r.status != 202 {
		t.Fatalf("stale claim: %d %s", r.status, r.raw)
	}

	// Revoking stops the key at once.
	id := created.body["key"].(map[string]any)["id"].(string)
	f.owner.must(200, f.owner.do("POST", "/admin/v1/brands/brightside/api-keys/"+id+"/revoke", nil))
	if r := apiCall(t, f.srv, "POST", "/api/v1/contacts", key, body); r.errCode() != "key_revoked" {
		t.Fatalf("revoked key: %d %s", r.status, r.raw)
	}

	// With a second admin, a new key waits for their approval.
	inv := f.owner.must(201, f.owner.do("POST", "/admin/v1/team/invites", map[string]any{"email": "bo@acme.test", "name": "Bo Owner", "company_role": "owner"}))
	bo := newBrowser(t, f.srv)
	bo.must(200, bo.acceptInvite(inv.body["invite_url"].(string)))
	pending, made := f.key(t, []string{"contacts:write"})
	k := made.body["key"].(map[string]any)
	if k["state"] != "pending_approval" || k["approval"] == nil {
		t.Fatalf("second-admin era key: %s", made.raw)
	}
	if r := apiCall(t, f.srv, "POST", "/api/v1/contacts", pending, body); r.status != 403 || r.errCode() != "key_pending_approval" {
		t.Fatalf("unapproved key: %d %s", r.status, r.raw)
	}
	f.owner.must(403, f.owner.do("POST", "/admin/v1/approvals/"+k["approval"].(map[string]any)["id"].(string)+"/approve", map[string]any{}))
	bo.must(200, bo.do("POST", "/admin/v1/approvals/"+k["approval"].(map[string]any)["id"].(string)+"/approve", map[string]any{}))
	if r := apiCall(t, f.srv, "POST", "/api/v1/contacts", pending, body); r.status != 202 && r.status != 200 {
		t.Fatalf("approved key: %d %s", r.status, r.raw)
	}

	// Only brand admins manage keys; viewers can't even list them.
	inv = f.owner.must(201, f.owner.do("POST", "/admin/v1/team/invites", map[string]any{
		"email": "vic@acme.test", "name": "Vic", "company_role": "member", "brand_roles": map[string]any{f.brandID.String(): "editor"}}))
	vic := newBrowser(t, f.srv)
	vic.must(200, vic.acceptInvite(inv.body["invite_url"].(string)))
	vic.must(403, vic.do("GET", "/admin/v1/brands/brightside/api-keys", nil))
	vic.must(403, vic.do("POST", "/admin/v1/brands/brightside/api-keys", map[string]any{"name": "x", "scopes": []string{"contacts:write"}}))
	vic.must(200, vic.do("GET", "/admin/v1/brands/brightside/consent-wordings", nil))

	// The spec is public.
	if r := apiCall(t, f.srv, "GET", "/api/v1/openapi.json", "", nil); r.status != 200 || r.body["openapi"] != "3.1.0" {
		t.Fatalf("openapi: %d", r.status)
	}
}

func TestRepermissionEmails(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	for _, e := range []string{"q1@example.com", "q2@example.com", "q3@example.com"} {
		if _, err := f.a.Store.InsertContact(ctx, store.NewContact{BrandID: f.brandID, Email: e, Status: store.ContactQuarantined, ConsentBasis: "single_opt_in"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.a.Store.Suppress(ctx, "q3@example.com", store.SuppressHardBounce, nil, "", nil); err != nil {
		t.Fatal(err)
	}
	// Off by default: nothing goes out.
	if err := f.a.Confirm.SendRepermissions(ctx); err != nil || len(outbox(f.a)) != 0 {
		t.Fatalf("re-permission while off: %v %d", err, len(outbox(f.a)))
	}
	on := f.owner.must(200, f.owner.do("PUT", "/admin/v1/brands/brightside/repermission", map[string]any{"on": true}))
	// q3 is suppressed: counted there, not as quarantined.
	if on.body["on"] != true || on.body["quarantined"].(float64) != 2 || on.body["per_day"].(float64) != 200 {
		t.Fatalf("turn on: %s", on.raw)
	}
	if err := f.a.Confirm.SendRepermissions(ctx); err != nil {
		t.Fatal(err)
	}
	// Suppressed addresses are never asked; each person once.
	if sentTo(f.a, "q1@example.com", "repermission") != 1 || sentTo(f.a, "q2@example.com", "repermission") != 1 || sentTo(f.a, "q3@example.com", "repermission") != 0 {
		t.Fatalf("outbox: %+v", outbox(f.a))
	}
	if err := f.a.Confirm.SendRepermissions(ctx); err != nil || sentTo(f.a, "q1@example.com", "repermission") != 1 {
		t.Fatal("asked twice")
	}
	for _, m := range outbox(f.a) {
		if m.Kind == "repermission" && !strings.HasPrefix(m.Headers["List-Unsubscribe"], "<") {
			t.Fatalf("re-permission email without List-Unsubscribe: %+v", m.Headers)
		}
	}
	page := newBrowser(t, f.srv)
	if p := page.do("POST", confirmLink(t, f.a, "q1@example.com", "repermission"), nil); !strings.Contains(string(p.raw), "You're subscribed") {
		t.Fatalf("re-permission confirm: %s", p.raw)
	}
	q1, _ := f.a.Store.ContactByEmail(ctx, f.brandID, "q1@example.com")
	if q1.Status != store.ContactSubscribed {
		t.Fatalf("q1: %s", q1.Status)
	}
	st := f.owner.must(200, f.owner.do("GET", "/admin/v1/brands/brightside/repermission", nil))
	if st.body["sent_today"].(float64) != 2 || st.body["quarantined"].(float64) != 1 {
		t.Fatalf("state: %s", st.raw)
	}
}

func TestRepermissionDailyLimitHoldsUnderConcurrency(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	for i := range 8 {
		if _, err := f.a.Store.InsertContact(ctx, store.NewContact{BrandID: f.brandID, Email: fmt.Sprintf("q%d@example.com", i), Status: store.ContactQuarantined, ConsentBasis: "single_opt_in"}); err != nil {
			t.Fatal(err)
		}
	}
	// Tightening a limit applies at once.
	f.owner.must(200, f.owner.do("PUT", "/admin/v1/safety-limits", map[string]any{"changes": map[string]any{"repermission_emails_per_day": 3}}))
	f.owner.must(200, f.owner.do("PUT", "/admin/v1/brands/brightside/repermission", map[string]any{"on": true}))
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { _ = f.a.Confirm.SendRepermissions(ctx) })
	}
	wg.Wait()
	n := 0
	for _, m := range outbox(f.a) {
		if m.Kind == "repermission" {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("4 parallel runs sent %d re-permission emails, daily limit 3", n)
	}
}

func TestSegmentsAPI(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	for _, e := range []string{"ann@example.com", "bob@example.org"} {
		if _, err := f.a.Store.InsertContact(ctx, store.NewContact{BrandID: f.brandID, Email: e, Status: store.ContactSubscribed, ConsentBasis: "double_opt_in", Country: "DE"}); err != nil {
			t.Fatal(err)
		}
	}
	rules := map[string]any{"match": "all", "groups": []any{map[string]any{"match": "all", "conditions": []any{
		map[string]any{"field": "email", "op": "ends_with", "value": "example.com"}}}}}
	p := f.owner.must(200, f.owner.do("POST", "/admin/v1/brands/brightside/segments/preview", map[string]any{"rules": rules}))
	if p.body["counts"].(map[string]any)["matching"].(float64) != 1 || len(p.body["sample"].([]any)) != 1 {
		t.Fatalf("preview: %s", p.raw)
	}
	s := f.owner.must(201, f.owner.do("POST", "/admin/v1/brands/brightside/segments", map[string]any{"name": "Dot com", "rules": rules}))
	f.owner.must(409, f.owner.do("POST", "/admin/v1/brands/brightside/segments", map[string]any{"name": "Dot com", "rules": rules}))
	if r := f.owner.do("POST", "/admin/v1/brands/brightside/segments/preview", map[string]any{"rules": map[string]any{"groups": []any{map[string]any{"conditions": []any{
		map[string]any{"field": "c.email) OR (1=1", "op": "equals", "value": "x"}}}}}}); r.status != 400 || r.errCode() != "invalid_rules" {
		t.Fatalf("bad field: %d %s", r.status, r.raw)
	}
	upd := f.owner.must(200, f.owner.do("PUT", "/admin/v1/brands/brightside/segments/"+s.body["id"].(string), map[string]any{"name": "Dot com", "rules": rules}))
	if upd.body["counts"].(map[string]any)["matching"].(float64) != 1 {
		t.Fatalf("update: %s", upd.raw)
	}
	list := f.owner.must(200, f.owner.do("GET", "/admin/v1/brands/brightside/segments", nil))
	if len(list.body["segments"].([]any)) != 1 {
		t.Fatalf("list: %s", list.raw)
	}
	id := s.body["id"].(string)
	inv := f.owner.must(201, f.owner.do("POST", "/admin/v1/team/invites", map[string]any{
		"email": "val@acme.test", "name": "Val", "company_role": "member", "brand_roles": map[string]any{f.brandID.String(): "viewer"}}))
	val := newBrowser(t, f.srv)
	val.must(200, val.acceptInvite(inv.body["invite_url"].(string)))
	// Viewers see saved segments' numbers but can't try rules of their own:
	// a count is a yes/no test ("email starts with a…") that spells out addresses.
	vg := val.must(200, val.do("GET", "/admin/v1/brands/brightside/segments/"+id, nil))
	if vg.body["counts"].(map[string]any)["matching"].(float64) != 1 {
		t.Fatalf("viewer get: %s", vg.raw)
	}
	val.must(403, val.do("POST", "/admin/v1/brands/brightside/segments/preview", map[string]any{"rules": rules}))
	if l := val.must(200, val.do("GET", "/admin/v1/brands/brightside/segments", nil)); l.body["segments"].([]any)[0].(map[string]any)["counts"] == nil {
		t.Fatalf("list should carry the cached count: %s", l.raw)
	}
	val.must(403, val.do("DELETE", "/admin/v1/brands/brightside/segments/"+id, nil))
	f.owner.must(204, f.owner.do("DELETE", "/admin/v1/brands/brightside/segments/"+id, nil))

	// Live counts are rate-limited per person.
	limited := 0
	for range 70 {
		if f.owner.do("POST", "/admin/v1/brands/brightside/segments/preview", map[string]any{"rules": rules}).status == 429 {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("70 previews in a minute weren't limited")
	}
}

// Brakes and the emails M7 sends: confirmations (wanted, transactional) stop
// only under the account brake or a paused brand; re-permission emails go
// out only while the brand's brake is off.
func TestBrakesGateConfirmationAndRepermission(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	key, _ := f.key(t, []string{"contacts:write"})
	for i := range 4 {
		if _, err := f.a.Store.InsertContact(ctx, store.NewContact{BrandID: f.brandID, Email: fmt.Sprintf("old%d@example.com", i), Status: store.ContactQuarantined, ConsentBasis: "single_opt_in"}); err != nil {
			t.Fatal(err)
		}
	}
	f.owner.must(200, f.owner.do("PUT", "/admin/v1/brands/brightside/repermission", map[string]any{"on": true}))
	repermissions := func() int {
		n := 0
		for _, m := range outbox(f.a) {
			if m.Kind == "repermission" {
				n++
			}
		}
		return n
	}
	set := func(brake, status string, accountBrake bool) {
		t.Helper()
		if err := f.a.Store.ExecForTest(ctx, `UPDATE brands SET brake_state = $2, status = $3 WHERE id = $1`, f.brandID, brake, status); err != nil {
			t.Fatal(err)
		}
		q := `UPDATE company_state SET account_brake_at = NULL, account_brake_reason = ''`
		if accountBrake {
			q = `UPDATE company_state SET account_brake_at = now(), account_brake_reason = 'test'`
		}
		if err := f.a.Store.ExecForTest(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	for i, tc := range []struct {
		brake, status string
		account       bool
		confirmation  bool
	}{
		{"watch", "ready", false, true},
		{"slowed", "ready", false, true},
		{"paused", "paused", false, false},
		{"healthy", "ready", true, false},
	} {
		set(tc.brake, tc.status, tc.account)
		email := fmt.Sprintf("signup%d@example.com", i)
		r := apiCall(t, f.srv, "POST", "/api/v1/contacts", key, f.signup(email))
		want := map[bool]string{true: "sent", false: "delayed"}[tc.confirmation]
		if r.status != 202 || r.body["confirmation_email"] != want {
			t.Fatalf("%s: signup %d %s, want confirmation_email %s", tc.brake, r.status, r.raw, want)
		}
		if got := sentTo(f.a, email, "confirmation") == 1; got != tc.confirmation {
			t.Errorf("brake %s, account brake %v: confirmation sent = %v", tc.brake, tc.account, got)
		}
		if err := f.a.Confirm.SendRepermissions(ctx); err != nil {
			t.Fatal(err)
		}
		if n := repermissions(); n != 0 {
			t.Fatalf("brake %s, account brake %v: %d re-permission emails", tc.brake, tc.account, n)
		}
	}
	set("healthy", "ready", false)
	// The held-back confirmations (paused brand, account brake) go out once
	// the gate opens, each once.
	retryHeld(t, f.a)
	retryHeld(t, f.a)
	for _, email := range []string{"signup2@example.com", "signup3@example.com"} {
		if n := sentTo(f.a, email, "confirmation"); n != 1 {
			t.Errorf("%s: %d confirmations after the gate opened", email, n)
		}
	}
	if err := f.a.Confirm.SendRepermissions(ctx); err != nil {
		t.Fatal(err)
	}
	if n := repermissions(); n != 4 {
		t.Fatalf("healthy brand: %d re-permission emails", n)
	}
}

// Signing up again and again while sending is stopped queues one job per
// try; when it resumes the person gets one confirmation, not a pile.
func TestHeldConfirmationsDontPileUp(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	key, _ := f.key(t, []string{"contacts:write"})
	if err := f.a.Store.ExecForTest(ctx, `UPDATE company_state SET account_brake_at = now(), account_brake_reason = 'test'`); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if r := apiCall(t, f.srv, "POST", "/api/v1/contacts", key, f.signup("ann@example.com")); r.body["confirmation_email"] != "delayed" {
			t.Fatalf("signup during the brake: %s", r.raw)
		}
	}
	if err := f.a.Store.ExecForTest(ctx, `UPDATE company_state SET account_brake_at = NULL, account_brake_reason = ''`); err != nil {
		t.Fatal(err)
	}
	retryHeld(t, f.a)
	if n := sentTo(f.a, "ann@example.com", "confirmation"); n != 1 {
		t.Fatalf("%d confirmation emails after the brake was lifted", n)
	}
}

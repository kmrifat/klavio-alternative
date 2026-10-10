package app_test

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/app"
	"github.com/kmrifat/klavio-alternative/api/internal/config"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/unsubscribe"
)

type public struct {
	t    *testing.T
	base string
}

// Recipients' mail clients: no cookies, no Origin header.
func (p public) do(method, path, contentType string, body io.Reader) (int, string, http.Header) {
	p.t.Helper()
	req, _ := http.NewRequest(method, p.base+path, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw), res.Header
}

func (p public) get(path string) (int, string) {
	code, body, _ := p.do("GET", path, "", nil)
	return code, body
}

func (p public) form(path string, v url.Values) (int, string) {
	code, body, _ := p.do("POST", path, "application/x-www-form-urlencoded", strings.NewReader(v.Encode()))
	return code, body
}

func (p public) oneClickMultipart(path string) int {
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	_ = mw.WriteField("List-Unsubscribe", "One-Click")
	_ = mw.Close()
	code, _, _ := p.do("POST", path, mw.FormDataContentType(), &b)
	return code
}

type unsubFixture struct {
	a                 *app.App
	pub               public
	owner             *browser
	brandID           uuid.UUID
	newsletter, promo uuid.UUID
	contactID         uuid.UUID
}

func newUnsubFixture(t *testing.T) unsubFixture { return newUnsubFixtureWith(t, nil) }

func newUnsubFixtureWith(t *testing.T, edit func(*config.Config)) unsubFixture {
	a, srv, _ := setupWith(t, edit)
	ctx := context.Background()
	owner := newBrowser(t, srv.URL)
	link, _ := a.Bootstrap(ctx, "owner@acme.test", "Olivia Owner")
	owner.must(200, owner.acceptInvite(link))
	b := owner.must(201, owner.do("POST", "/admin/v1/brands", brightside)).body["brand"].(map[string]any)
	base := "/admin/v1/brands/brightside"
	topics := owner.must(200, owner.do("GET", base+"/topics", nil)).body["topics"].([]any)
	newsletter := topics[0].(map[string]any)["id"].(string)
	promo := owner.must(201, owner.do("POST", base+"/topics", map[string]any{"name": "Product updates"})).body["id"].(string)
	c := owner.must(201, owner.do("POST", base+"/contacts", map[string]any{
		"email": "maya@example.com", "first_name": "Maya", "topic_ids": []string{newsletter, promo},
		"consent": map[string]string{"basis": "double_opt_in", "source": "Signup form"},
	})).body
	return unsubFixture{
		a: a, pub: public{t: t, base: srv.URL}, owner: owner,
		brandID: uuid.MustParse(b["id"].(string)), newsletter: uuid.MustParse(newsletter), promo: uuid.MustParse(promo),
		contactID: uuid.MustParse(c["id"].(string)),
	}
}

func (f unsubFixture) claims(topic, campaign uuid.UUID) unsubscribe.Claims {
	return unsubscribe.Claims{BrandID: f.brandID, ContactID: f.contactID, TopicID: topic, CampaignID: campaign}
}

func (f unsubFixture) path(prefix string, c unsubscribe.Claims) string {
	return prefix + f.a.Unsubscribe.Token(c)
}

func (f unsubFixture) state(t *testing.T) (store.ContactStatus, []uuid.UUID, int) {
	t.Helper()
	ctx := context.Background()
	c, err := f.a.Store.ContactByID(ctx, f.brandID, f.contactID)
	if err != nil {
		t.Fatal(err)
	}
	topics, _ := f.a.Store.ContactTopicIDs(ctx, f.contactID)
	history, _ := f.a.Store.ConsentHistory(ctx, f.contactID)
	return c.Status, topics, len(history)
}

func TestGETNeverUnsubscribes(t *testing.T) {
	f := newUnsubFixture(t)
	c := f.claims(f.newsletter, uuid.New())
	for _, p := range []string{f.path("/u/", c), f.path("/p/", c), f.path("/u/", f.claims(uuid.Nil, uuid.Nil))} {
		for range 3 { // link scanners open links repeatedly
			if code, _ := f.pub.get(p); code != 200 {
				t.Fatalf("GET %s: %d", p[:3], code)
			}
		}
	}
	status, topics, records := f.state(t)
	if status != store.ContactSubscribed || len(topics) != 2 || records != 1 {
		t.Fatalf("GETs changed the contact: %s %d topics %d records", status, len(topics), records)
	}
	_, page := f.pub.get(f.path("/u/", c))
	if !strings.Contains(page, "Unsubscribe from Newsletter?") || !strings.Contains(page, "m•••@example.com") || strings.Contains(page, "maya@example.com") {
		t.Fatalf("confirm page content wrong:\n%s", page)
	}
}

func TestOneClickUnsubscribe(t *testing.T) {
	f := newUnsubFixture(t)
	ctx := context.Background()
	campaign := uuid.New()
	nl := f.path("/u/", f.claims(f.newsletter, campaign))

	// RFC 8058 form-encoded POST removes only the campaign's topic.
	code, body := f.pub.form(nl, url.Values{"List-Unsubscribe": {"One-Click"}})
	if code != 200 || !strings.Contains(body, "Unsubscribed") {
		t.Fatalf("one-click: %d %q", code, body)
	}
	status, topics, records := f.state(t)
	if status != store.ContactSubscribed || len(topics) != 1 || topics[0] != f.promo || records != 2 {
		t.Fatalf("after one-click: %s %v %d", status, topics, records)
	}
	if n, _ := f.a.Store.CampaignUnsubscribes(ctx, campaign); n != 1 {
		t.Fatalf("campaign unsubscribes = %d", n)
	}

	// Idempotent: again → 200, no new record.
	if code, _ := f.pub.form(nl, url.Values{"List-Unsubscribe": {"One-Click"}}); code != 200 {
		t.Fatalf("repeat: %d", code)
	}
	if _, _, r := f.state(t); r != 2 {
		t.Fatalf("repeat added a record: %d", r)
	}

	// Multipart one-click for the last topic → fully unsubscribed.
	if code := f.pub.oneClickMultipart(f.path("/u/", f.claims(f.promo, uuid.Nil))); code != 200 {
		t.Fatalf("multipart one-click: %d", code)
	}
	if status, topics, _ := f.state(t); status != store.ContactUnsubscribed || len(topics) != 0 {
		t.Fatalf("after last topic: %s %v", status, topics)
	}
	history, _ := f.a.Store.ConsentHistory(ctx, f.contactID)
	if history[0].Kind != "unsubscribed" || history[0].Method != "header" {
		t.Fatalf("latest record: %+v", history[0])
	}
}

func TestUnsubscribePageAndPreferences(t *testing.T) {
	f := newUnsubFixture(t)
	c := f.claims(f.newsletter, uuid.Nil)

	// Page confirm: just this topic.
	code, page := f.pub.form(f.path("/u/", c), url.Values{"scope": {"topic"}})
	if code != 200 || !strings.Contains(page, "won't get Newsletter emails") {
		t.Fatalf("confirm topic: %d\n%s", code, page)
	}
	// Preferences show it unchecked; Product updates still checked.
	_, prefs := f.pub.get(f.path("/p/", c))
	if !strings.Contains(prefs, `value="`+f.promo.String()+`" checked`) || strings.Contains(prefs, `value="`+f.newsletter.String()+`" checked`) {
		t.Fatalf("preferences page:\n%s", prefs)
	}
	// Unsubscribe from everything, then opt back in to Newsletter.
	f.pub.form(f.path("/p/", c), url.Values{"action": {"unsubscribe_all"}})
	if status, topics, _ := f.state(t); status != store.ContactUnsubscribed || len(topics) != 0 {
		t.Fatalf("after unsubscribe all: %s %v", status, topics)
	}
	code, prefs = f.pub.form(f.path("/p/", c), url.Values{"action": {"save"}, "topic": {f.newsletter.String(), "not-a-uuid", uuid.NewString()}})
	if code != 200 || !strings.Contains(prefs, "Saved") {
		t.Fatalf("save: %d\n%s", code, prefs)
	}
	status, topics, _ := f.state(t)
	if status != store.ContactSubscribed || len(topics) != 1 || topics[0] != f.newsletter {
		t.Fatalf("after resubscribe: %s %v", status, topics)
	}
	history, _ := f.a.Store.ConsentHistory(context.Background(), f.contactID)
	if history[0].Kind != "resubscribed" || history[0].Method != "link" {
		t.Fatalf("latest record: %+v", history[0])
	}
}

func TestSuppressedCanOnlyRemove(t *testing.T) {
	f := newUnsubFixture(t)
	c := f.claims(uuid.Nil, uuid.Nil)
	f.owner.must(204, f.owner.do("POST", "/admin/v1/brands/brightside/contacts/"+f.contactID.String()+"/suppress", map[string]any{"note": "complaint by phone"}))

	_, prefs := f.pub.get(f.path("/p/", c))
	if !strings.Contains(prefs, "can't receive email from us") {
		t.Fatalf("suppressed preferences page:\n%s", prefs)
	}
	// Removing a topic works.
	if code, _ := f.pub.form(f.path("/p/", c), url.Values{"action": {"save"}, "topic": {f.newsletter.String()}}); code != 200 {
		t.Fatalf("remove while suppressed: %d", code)
	}
	// Adding one is refused.
	code, _ := f.pub.form(f.path("/p/", c), url.Values{"action": {"save"}, "topic": {f.newsletter.String(), f.promo.String()}})
	if _, topics, _ := f.state(t); code != 409 || len(topics) != 1 {
		t.Fatalf("add while suppressed: %d, topics %v", code, topics)
	}
}

func TestInvalidTokensRevealNothing(t *testing.T) {
	f := newUnsubFixture(t)
	good := f.a.Unsubscribe.Token(f.claims(f.newsletter, uuid.Nil))
	tampered := good[:len(good)-3] + "AAA"
	other, _ := unsubscribe.NewSigner("someone-elses-secret-0123456789abcdef")
	forged := other.Token(f.claims(f.newsletter, uuid.Nil))
	wrongBrand := f.a.Unsubscribe.Token(unsubscribe.Claims{BrandID: uuid.New(), ContactID: f.contactID})

	for name, tok := range map[string]string{"tampered": tampered, "forged": forged, "garbage": "nope", "unknown brand": wrongBrand} {
		for _, prefix := range []string{"/u/", "/p/"} {
			code, page := f.pub.get(prefix + tok)
			if code != 404 || !strings.Contains(page, "This link isn't valid") || strings.Contains(page, "Brightside") || strings.Contains(page, "example.com") {
				t.Errorf("%s GET %s: %d", name, prefix, code)
			}
			if code, _ := f.pub.form(prefix+tok, url.Values{"List-Unsubscribe": {"One-Click"}}); code != 404 {
				t.Errorf("%s POST %s: %d", name, prefix, code)
			}
		}
	}
	if status, topics, _ := f.state(t); status != store.ContactSubscribed || len(topics) != 2 {
		t.Fatal("an invalid token changed the contact")
	}

	// A genuine token for someone who's no longer a contact: a normal page.
	gone := f.a.Unsubscribe.Token(unsubscribe.Claims{BrandID: f.brandID, ContactID: uuid.New()})
	if code, page := f.pub.get("/u/" + gone); code != 200 || !strings.Contains(page, "isn't on Brightside's list") {
		t.Fatalf("unknown contact: %d", code)
	}
	if code, _ := f.pub.form("/u/"+gone, url.Values{"List-Unsubscribe": {"One-Click"}}); code != 200 {
		t.Fatalf("one-click for unknown contact should still succeed: %d", code)
	}
}

func TestPublicPagesHeadersAndRateLimit(t *testing.T) {
	f := newUnsubFixture(t)
	p := f.path("/u/", f.claims(f.newsletter, uuid.Nil))
	_, _, h := f.pub.do("GET", p, "", nil)
	for k, want := range map[string]string{
		"Referrer-Policy": "no-referrer", "Cache-Control": "no-store", "X-Robots-Tag": "noindex, nofollow",
	} {
		if h.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, h.Get(k), want)
		}
	}
	if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP = %q", csp)
	}

	limited := false // 10 per minute per contact
	for range 15 {
		if code, _ := f.pub.form(p, url.Values{"List-Unsubscribe": {"One-Click"}}); code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("15 POSTs for one contact in a minute were never rate-limited")
	}
}

// newContact inserts a contact with any status, subscribed to the given topics.
func (f unsubFixture) newContact(t *testing.T, email string, status store.ContactStatus, topics ...uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id, err := f.a.Store.InsertContact(ctx, store.NewContact{
		BrandID: f.brandID, Email: email, Status: status, ConsentBasis: "double_opt_in", TrackingAllowed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) > 0 {
		if err := f.a.Store.AddContactTopics(ctx, id, topics); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// Review 1: Gmail sends one-click POSTs for many people from one IP.
func TestOneClickFromSharedMailServer(t *testing.T) {
	f := newUnsubFixture(t)
	for i := range 40 {
		id := f.newContact(t, "person"+uuid.NewString()[:8]+"@example.com", store.ContactSubscribed, f.newsletter)
		tok := f.a.Unsubscribe.Token(unsubscribe.Claims{BrandID: f.brandID, ContactID: id, TopicID: f.newsletter})
		req, _ := http.NewRequest("POST", f.pub.base+"/u/"+tok, strings.NewReader("List-Unsubscribe=One-Click"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("CF-Connecting-IP", "209.85.220.41") // a Google mail server
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("one-click #%d from a shared mail server: %d", i+1, res.StatusCode)
		}
	}
}

// Review 2: the preference center must not skip double opt-in or quarantine.
func TestPreferencesCantBypassConfirmation(t *testing.T) {
	f := newUnsubFixture(t)
	ctx := context.Background()
	for _, status := range []store.ContactStatus{store.ContactPending, store.ContactQuarantined} {
		t.Run(string(status), func(t *testing.T) {
			id := f.newContact(t, string(status)+"@example.com", status, f.newsletter)
			c := unsubscribe.Claims{BrandID: f.brandID, ContactID: id}
			path := "/p/" + f.a.Unsubscribe.Token(c)

			// Adding a topic directly is refused.
			if code, _ := f.pub.form(path, url.Values{"action": {"save"}, "topic": {f.newsletter.String(), f.promo.String()}}); code != 409 {
				t.Fatalf("direct add: %d", code)
			}
			// Unsubscribe from all, then try to come back: still refused, still unsubscribed.
			if code, _ := f.pub.form(path, url.Values{"action": {"unsubscribe_all"}}); code != 200 {
				t.Fatalf("unsubscribe all: %d", code)
			}
			code, page := f.pub.form(path, url.Values{"action": {"save"}, "topic": {f.newsletter.String()}})
			got, _ := f.a.Store.ContactByID(ctx, f.brandID, id)
			topics, _ := f.a.Store.ContactTopicIDs(ctx, id)
			if code != 409 || got.Status != store.ContactUnsubscribed || len(topics) != 0 {
				t.Fatalf("re-add after unsubscribe: %d, status %s, topics %v", code, got.Status, topics)
			}
			if !strings.Contains(page, "confirm your subscription") || !strings.Contains(page, "disabled") {
				t.Fatal("page should explain and disable adding")
			}
		})
	}

	// A properly subscribed contact who unsubscribed can come back (the main flow).
	c := f.claims(uuid.Nil, uuid.Nil)
	f.pub.form(f.path("/p/", c), url.Values{"action": {"unsubscribe_all"}})
	if code, _ := f.pub.form(f.path("/p/", c), url.Values{"action": {"save"}, "topic": {f.newsletter.String()}}); code != 200 {
		t.Fatalf("subscribed contact coming back: %d", code)
	}
	if status, _, _ := f.state(t); status != store.ContactSubscribed {
		t.Fatalf("status %s", status)
	}
}

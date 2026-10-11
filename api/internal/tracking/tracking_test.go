package tracking

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/testdb"
	"github.com/kmrifat/klavio-alternative/api/internal/unsubscribe"
)

const secret = "tracking-test-secret-aaaaaaaaaaaaaaaaaaaa"

func TestTokens(t *testing.T) {
	s, _ := NewSigner(secret)
	l := Link{BrandID: uuid.New(), ContactID: uuid.New(), SendID: 1234567890123, LinkNo: 2, LinkCount: 5,
		URL: "https://brightside.co/sale?utm_source=email&x=ü", Flags: NoTrack}
	tok, err := s.ClickToken(l)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Click(tok)
	if err != nil || got != l {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	// The destination is signed: changing it breaks the token (no open redirect).
	raw, _ := b64.DecodeString(tok)
	i := strings.Index(string(raw), "brightside.co")
	copy(raw[i:], "evil-site.co!")
	if _, err := s.Click(b64.EncodeToString(raw)); !errors.Is(err, ErrSignature) {
		t.Fatalf("swapped destination: %v", err)
	}
	for _, bad := range []string{"javascript:alert(1)", "//evil.example/x", "ftp://x.example/", "https://x.example/\r\nSet-Cookie: a=b"} {
		if _, err := s.ClickToken(Link{URL: bad, LinkNo: 1, LinkCount: 1}); !errors.Is(err, ErrBadURL) {
			t.Errorf("%q signed", bad)
		}
	}
	px := Pixel{BrandID: l.BrandID, ContactID: l.ContactID, SendID: 7}
	if got, err := s.Open(s.OpenToken(px)); err != nil || got != px {
		t.Fatalf("pixel: %+v %v", got, err)
	}
	// Kinds don't mix: an open token isn't a click, an unsubscribe token isn't either.
	if _, err := s.Click(s.OpenToken(px)); err == nil {
		t.Fatal("open token accepted as a click")
	}
	u, _ := unsubscribe.NewSigner(secret)
	if _, err := s.Open(u.Token(unsubscribe.Claims{BrandID: l.BrandID, ContactID: l.ContactID})); err == nil {
		t.Fatal("unsubscribe token accepted as an open")
	}
}

func TestClassifier(t *testing.T) {
	c := Classifier{ScannerNets: []netip.Prefix{netip.MustParsePrefix("40.94.0.0/16")}}
	now := time.Now()
	chrome := "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0 Safari/537.36"
	human := Hit{At: now, Method: "GET", UserAgent: chrome, IP: netip.MustParseAddr("203.0.113.9"), LinkNo: 1, LinkCount: 4}
	delivered := now.Add(-5 * time.Minute)
	human.DeliveredAt = &delivered
	if bot, r, _ := c.Click(human, nil); bot {
		t.Fatalf("human click marked bot: %s", r)
	}
	soon := now.Add(-3 * time.Second)
	cases := map[string]Hit{
		"honeypot_link":           {At: now, Method: "GET", UserAgent: chrome, Flags: Honeypot, LinkNo: 1, LinkCount: 4},
		"head_request":            {At: now, Method: "HEAD", UserAgent: chrome, LinkNo: 1, LinkCount: 4},
		"no_user_agent":           {At: now, Method: "GET", LinkNo: 1, LinkCount: 4},
		"scanner_user_agent":      {At: now, Method: "GET", UserAgent: "Mozilla/5.0 (compatible; Barracuda Sentinel)", LinkNo: 1, LinkCount: 4},
		"scanner_ip":              {At: now, Method: "GET", UserAgent: chrome, IP: netip.MustParseAddr("40.94.1.2"), LinkNo: 1, LinkCount: 4},
		"too_fast_after_delivery": {At: now, Method: "GET", UserAgent: chrome, DeliveredAt: &soon, LinkNo: 1, LinkCount: 4},
	}
	for want, h := range cases {
		if bot, r, _ := c.Click(h, nil); !bot || r != want {
			t.Errorf("%s: bot=%v reason=%q", want, bot, r)
		}
	}
	// Three links within seconds: a scanner walking the email, earlier clicks included.
	prior := []PriorClick{{ID: 1, At: now.Add(-2 * time.Second), LinkNo: 2}, {ID: 2, At: now.Add(-time.Second), LinkNo: 3}}
	if bot, r, burst := c.Click(human, prior); !bot || r != "many_links_at_once" || len(burst) != 2 {
		t.Fatalf("burst: %v %q %v", bot, r, burst)
	}
	// The same link twice, or links minutes apart, is a person.
	if bot, _, _ := c.Click(human, []PriorClick{{ID: 1, At: now.Add(-2 * time.Second), LinkNo: 1}}); bot {
		t.Fatal("double-click marked bot")
	}
	if bot, _, _ := c.Click(human, []PriorClick{{ID: 1, At: now.Add(-time.Minute), LinkNo: 2}, {ID: 2, At: now.Add(-30 * time.Second), LinkNo: 3}}); bot {
		t.Fatal("reading and clicking marked bot")
	}

	if m, r := c.Open(Hit{At: now, Method: "GET", UserAgent: "Mozilla/5.0"}); !m || r != "apple_privacy_proxy" {
		t.Fatalf("apple MPP: %v %q", m, r)
	}
	if m, _ := c.Open(Hit{At: now, Method: "GET", UserAgent: chrome, IP: netip.MustParseAddr("203.0.113.9")}); m {
		t.Fatal("human open marked machine")
	}
}

type fixture struct {
	st      *store.Store
	signer  *Signer
	srv     *httptest.Server
	brand   store.Brand
	contact uuid.UUID
	send    int64
	rec     *Recorder
}

// settle makes every click so far old enough to settle, then settles.
func (f fixture) settle(t *testing.T) store.Contact {
	t.Helper()
	ctx := context.Background()
	if err := f.st.ExecForTest(ctx, `UPDATE clicks SET at = at - interval '2 minutes', recorded_at = recorded_at - interval '2 minutes' WHERE settled_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	if err := f.rec.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	c, err := f.st.ContactByID(ctx, f.brand.ID, f.contact)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func setup(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	st := store.New(testdb.New(t))
	b, err := st.CreateBrand(ctx, store.Brand{Slug: "acme", Name: "Acme", Color: "#000000", PostalAddress: "1 Main St",
		SendingDomain: "news.acme.test", MailFromDomain: "bounce.news.acme.test", LinkDomain: "go.acme.test", SESConfigurationSet: "brand-acme"})
	if err != nil {
		t.Fatal(err)
	}
	cid, err := st.InsertContact(ctx, store.NewContact{BrandID: b.ID, Email: "ann@example.com", Status: store.ContactSubscribed,
		ConsentBasis: "double_opt_in", TrackingAllowed: true})
	if err != nil {
		t.Fatal(err)
	}
	send, err := st.CreateSendForTest(ctx, b.ID, cid)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := NewSigner(secret)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	rec := &Recorder{Store: st, Log: log}
	(&Handlers{Signer: s, Recorder: rec, Log: log}).Routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return fixture{st: st, signer: s, srv: srv, brand: b, contact: cid, send: send, rec: rec}
}

var noFollow = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (f fixture) get(t *testing.T, method, path, ua string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+path, nil)
	req.Header.Set("User-Agent", ua)
	res, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res
}

func (f fixture) link(t *testing.T, no int, flags byte) string {
	t.Helper()
	tok, err := f.signer.ClickToken(Link{BrandID: f.brand.ID, ContactID: f.contact, SendID: f.send, LinkNo: no, LinkCount: 3,
		URL: "https://acme.test/p/" + string(rune('a'+no)), Flags: flags})
	if err != nil {
		t.Fatal(err)
	}
	return ClickPath + tok
}

func (f fixture) clicks(t *testing.T) (humans, bots int) {
	t.Helper()
	rows, err := f.st.ClicksSince(context.Background(), f.send, time.Time{}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.IsBot {
			bots++
		} else {
			humans++
		}
	}
	return
}

const chrome = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0 Safari/537.36"

func TestRedirectAndRecording(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	res := f.get(t, "GET", f.link(t, 1, 0), chrome)
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "https://acme.test/p/b" || res.Header.Get("Cache-Control") != "no-store, private" {
		t.Fatalf("redirect: %d %v", res.StatusCode, res.Header)
	}
	if h, b := f.clicks(t); h != 1 || b != 0 {
		t.Fatalf("first click: %d humans %d bots", h, b)
	}
	// Not engagement yet: later clicks could still show it was a scanner.
	if c, _ := f.st.ContactByID(ctx, f.brand.ID, f.contact); c.LastClickAt != nil {
		t.Fatal("a click counted before it settled")
	}

	if res := f.get(t, "GET", ClickPath+"not-a-token", chrome); res.StatusCode != 404 {
		t.Fatalf("bad token: %d", res.StatusCode)
	}
	// A token for a send that isn't ours (deleted): redirect, record nothing.
	other, _ := f.signer.ClickToken(Link{BrandID: f.brand.ID, ContactID: f.contact, SendID: f.send + 1000, LinkNo: 1, LinkCount: 1, URL: "https://acme.test/x"})
	if res := f.get(t, "GET", ClickPath+other, chrome); res.StatusCode != 302 {
		t.Fatalf("unknown send: %d", res.StatusCode)
	}
	// No tracking consent: the link works, nothing is stored.
	if res := f.get(t, "GET", f.link(t, 2, NoTrack), chrome); res.StatusCode != 302 {
		t.Fatalf("no-track link: %d", res.StatusCode)
	}
	if h, b := f.clicks(t); h+b != 1 {
		t.Fatal("a no-track click was recorded")
	}
	// HEAD (scanners checking links) redirects but counts as a bot.
	if res := f.get(t, "HEAD", f.link(t, 1, 0), chrome); res.StatusCode != 302 {
		t.Fatalf("HEAD: %d", res.StatusCode)
	}
	// Links 2 and 3 within seconds of link 1 → all three were a scanner.
	f.get(t, "GET", f.link(t, 2, 0), chrome)
	f.get(t, "GET", f.link(t, 3, 0), chrome)
	if h, b := f.clicks(t); h != 0 || b != 4 {
		t.Fatalf("after a burst: %d humans %d bots", h, b)
	}
	// The scanner walking every link a minute after delivery moves no tier.
	if c := f.settle(t); c.LastClickAt != nil {
		t.Fatalf("scanner clicks became engagement: %v", c.LastClickAt)
	}
	// A person's lone click does, once settled.
	f.get(t, "GET", f.link(t, 2, 0), chrome)
	if c := f.settle(t); c.LastClickAt == nil || c.Tier != store.TierActive {
		t.Fatalf("a settled human click is engagement: %+v", c)
	}

	// Pixel: always a GIF; Apple's proxy is a machine open.
	res = f.get(t, "GET", OpenPath+f.signer.OpenToken(Pixel{BrandID: f.brand.ID, ContactID: f.contact, SendID: f.send})+".gif", "Mozilla/5.0")
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/gif" {
		t.Fatalf("pixel: %d %v", res.StatusCode, res.Header)
	}
	if res := f.get(t, "GET", OpenPath+"junk.gif", chrome); res.StatusCode != 200 {
		t.Fatalf("bad pixel token should still return a pixel: %d", res.StatusCode)
	}
}

func TestTooFastAfterDelivery(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.st.InsertEmailEvent(ctx, store.EmailEvent{EventID: "d1", Type: "delivery", BrandID: &f.brand.ID, SESMessageID: "m1",
		SendID: &f.send, EmailHash: store.EmailHash("ann@example.com"), OccurredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	f.get(t, "GET", f.link(t, 1, 0), chrome)
	if h, b := f.clicks(t); h != 0 || b != 1 {
		t.Fatalf("click a second after delivery: %d humans %d bots", h, b)
	}
	c, _ := f.st.ContactByID(ctx, f.brand.ID, f.contact)
	if c.LastClickAt != nil {
		t.Fatal("a scanner click counted as engagement")
	}
}

func TestHoneypotTaintsTheVisitorsClicks(t *testing.T) {
	f := setup(t)
	f.get(t, "GET", f.link(t, 1, 0), chrome)
	tok, err := f.signer.ClickToken(Link{BrandID: f.brand.ID, ContactID: f.contact, SendID: f.send, LinkNo: 0, LinkCount: 3,
		URL: "https://go.acme.test/", Flags: Honeypot})
	if err != nil {
		t.Fatal(err)
	}
	f.get(t, "GET", ClickPath+tok, chrome)
	if h, b := f.clicks(t); h != 0 || b != 2 {
		t.Fatalf("after the honeypot: %d humans %d bots", h, b)
	}
	if c := f.settle(t); c.LastClickAt != nil {
		t.Fatal("a scanner's click became engagement")
	}
	// Honeypot tokens are link 0; real links can't be.
	if _, err := f.signer.ClickToken(Link{SendID: 1, LinkNo: 0, LinkCount: 1, URL: "https://x.test/"}); err == nil {
		t.Fatal("link 0 without the honeypot flag")
	}
}

func TestHoneypotClassifier(t *testing.T) {
	now := time.Now()
	ip := netip.MustParseAddr("198.51.100.4")
	prior := []PriorClick{
		{ID: 1, At: now.Add(-5 * time.Second), LinkNo: 1, IP: "198.51.100.4", UserAgent: "x"},
		{ID: 2, At: now.Add(-5 * time.Second), LinkNo: 2, IP: "203.0.113.1", UserAgent: "someone else"}, // another visitor
		{ID: 3, At: now.Add(-20 * time.Minute), LinkNo: 1, IP: "198.51.100.4", UserAgent: "x"},          // too long ago
	}
	bot, reason, burst := Classifier{}.Click(Hit{At: now, Method: "GET", UserAgent: "x", IP: ip, Flags: Honeypot}, prior)
	if !bot || reason != "honeypot_link" || len(burst) != 1 || burst[0] != 1 {
		t.Fatalf("honeypot: %v %q %v", bot, reason, burst)
	}
	// A click after the same visitor hit the honeypot is a bot too; someone
	// else's honeypot hit doesn't count against this click.
	h := Hit{At: now, Method: "GET", UserAgent: "Mozilla/5.0 (X11) Chrome/129", IP: ip, LinkNo: 1, LinkCount: 2}
	if bot, r, _ := (Classifier{}).Click(h, []PriorClick{{ID: 9, At: now.Add(-time.Second), LinkNo: 0, IP: "198.51.100.4"}}); !bot || r != "honeypot_link" {
		t.Fatalf("after the honeypot: %v %q", bot, r)
	}
	if bot, _, _ := (Classifier{}).Click(h, []PriorClick{{ID: 9, At: now.Add(-time.Second), LinkNo: 0, IP: "203.0.113.50", UserAgent: "other"}}); bot {
		t.Fatal("another visitor's honeypot hit marked this click")
	}
	// The same browser from another network is someone else; the same /24 is the same scanner.
	h.UserAgent = "Mozilla/5.0 same browser"
	if bot, _, _ := (Classifier{}).Click(h, []PriorClick{{ID: 9, At: now.Add(-time.Second), LinkNo: 0, IP: "203.0.113.50", UserAgent: h.UserAgent}}); bot {
		t.Fatal("a matching user agent alone tainted the click")
	}
	if bot, _, _ := (Classifier{}).Click(h, []PriorClick{{ID: 9, At: now.Add(-time.Second), LinkNo: 0, IP: "198.51.100.200"}}); !bot {
		t.Fatal("the same /24 as the honeypot hit wasn't tainted")
	}
	h.IP = netip.MustParseAddr("2001:db8:1:2::99")
	if bot, _, _ := (Classifier{}).Click(h, []PriorClick{{ID: 9, At: now.Add(-time.Second), LinkNo: 0, IP: "2001:db8:1:2::1"}}); !bot {
		t.Fatal("the same /64 as the honeypot hit wasn't tainted")
	}
	if bot, _, _ := (Classifier{}).Click(h, []PriorClick{{ID: 9, At: now.Add(-time.Second), LinkNo: 0, IP: "2001:db8:1:3::1"}}); bot {
		t.Fatal("another /64 was tainted")
	}
}

// click records a click directly, as the recorder would after a lag: it
// happened at `at` but is written now.
func (f fixture) clickAt(t *testing.T, at time.Time, linkNo int, flags byte) {
	t.Helper()
	l := Link{BrandID: f.brand.ID, ContactID: f.contact, SendID: f.send, LinkNo: linkNo, LinkCount: 3, URL: "https://acme.test/x", Flags: flags}
	if err := f.rec.Click(context.Background(), l, Hit{At: at, Method: "GET", UserAgent: chrome, IP: netip.MustParseAddr("198.51.100.7"),
		LinkNo: linkNo, LinkCount: 3, Flags: flags}); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) lastClick(t *testing.T) *time.Time {
	t.Helper()
	c, err := f.st.ContactByID(context.Background(), f.brand.ID, f.contact)
	if err != nil {
		t.Fatal(err)
	}
	return c.LastClickAt
}

// (a) The recorder lags: a click from 58 s ago is written just now. It
// doesn't settle while its email is still getting clicks, so the rest of
// the burst arriving later still unmasks it.
func TestLaggingRecorderDoesNotSettleEarly(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	start := time.Now().Add(-58 * time.Second)
	f.clickAt(t, start, 1, 0)
	if err := f.rec.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	if f.lastClick(t) != nil {
		t.Fatal("a click written seconds ago settled because it happened a minute ago")
	}
	f.clickAt(t, start.Add(time.Second), 2, 0)
	f.clickAt(t, start.Add(2*time.Second), 3, 0)
	if c := f.settle(t); c.LastClickAt != nil {
		t.Fatalf("a lagged scanner burst became engagement: %v", c.LastClickAt)
	}
	if h, b := f.clicks(t); h != 0 || b != 3 {
		t.Fatalf("%d humans %d bots", h, b)
	}
}

// (b) A click that already counted is unmasked later (a honeypot hit from
// the same network a minute after it settled, or the rest of a burst): it
// flips to a bot and the contact's engagement is rebuilt from what's left.
func TestSettledClickUnmaskedLater(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	// Evidence from an import that must survive: the old tool's last click.
	imported := time.Now().Add(-100 * 24 * time.Hour).Truncate(time.Second)
	if err := f.st.ExecForTest(ctx, `UPDATE contacts SET imported_click_at = $2, last_click_at = $2, last_engaged_at = $2 WHERE id = $1`, f.contact, imported); err != nil {
		t.Fatal(err)
	}
	first := time.Now().Add(-61 * time.Second)
	f.clickAt(t, first, 1, 0)
	if c := f.settle(t); c.LastClickAt == nil || c.LastClickAt.Before(first.Add(-3*time.Minute)) || c.Tier != store.TierActive {
		t.Fatalf("settled human click: %+v", c)
	}
	// The honeypot, 60 s after the click (it was moved 2 minutes back to settle).
	f.clickAt(t, time.Now(), 0, Honeypot)
	c, _ := f.st.ContactByID(ctx, f.brand.ID, f.contact)
	if c.LastClickAt == nil || !c.LastClickAt.Equal(imported) || c.Tier != store.TierAtRisk {
		t.Fatalf("engagement not rebuilt from the remaining evidence: last click %v, tier %s", c.LastClickAt, c.Tier)
	}
	if h, b := f.clicks(t); h != 0 || b != 2 {
		t.Fatalf("%d humans %d bots", h, b)
	}
}

// A replayed link stops being stored once the email has had more clicks in
// the window than any person makes; a huge User-Agent is cut before it's queued.
func TestReplayedLinkStopsRecording(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for i := range MaxRecentClicks + 1 {
		if _, err := f.st.InsertClick(ctx, store.Click{SendID: f.send, BrandID: f.brand.ID, ContactID: f.contact, LinkNo: 1,
			URL: "https://acme.test/p/b", At: time.Now().Add(-time.Duration(i) * time.Millisecond), Method: "GET"}); err != nil {
			t.Fatal(err)
		}
	}
	f.get(t, "GET", f.link(t, 1, 0), chrome)
	if h, b := f.clicks(t); h+b != MaxRecentClicks+1 {
		t.Fatalf("replayed click recorded: %d clicks", h+b)
	}

	r := httptest.NewRequest("GET", "/c/x", nil)
	r.Header.Set("User-Agent", strings.Repeat("A", 100_000))
	if h := (&Handlers{}).hit(r); len(h.UserAgent) != 500 {
		t.Fatalf("user agent kept at %d bytes", len(h.UserAgent))
	}
}

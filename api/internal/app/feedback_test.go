package app_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kmrifat/klavio-alternative/api/internal/events"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/tracking"
)

func TestFeedbackLoop(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	b, _ := f.a.Store.BrandByID(ctx, f.brandID)
	annID, err := f.a.Store.InsertContact(ctx, store.NewContact{BrandID: f.brandID, Email: "ann@example.com", Status: store.ContactSubscribed,
		ConsentBasis: "double_opt_in", TrackingAllowed: true})
	if err != nil {
		t.Fatal(err)
	}

	// A hard bounce arrives through SQS → suppressed everywhere, with a consent record.
	q := &events.Fake{}
	q.Push([]byte(fmt.Sprintf(`{"id":"ev-1","source":"aws.ses","time":"2026-10-10T09:00:00Z","detail":{"eventType":"Bounce",
		"mail":{"messageId":"m-1","destination":["ann@example.com"],"tags":{"ses:configuration-set":[%q],"send_id":["7"]}},
		"bounce":{"bounceType":"Permanent","bounceSubType":"General","bouncedRecipients":[{"emailAddress":"ann@example.com"}]}}}`, b.SESConfigurationSet)))
	r := &events.Reader{Queue: q, Processor: f.a.Events, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if n, err := r.Once(ctx); err != nil || n != 1 {
		t.Fatalf("reader: %d %v", n, err)
	}
	c := f.owner.must(200, f.owner.do("GET", "/admin/v1/brands/brightside/contacts/"+annID.String(), nil))
	if !strings.Contains(string(c.raw), `"suppression_reason":"hard_bounce"`) || !strings.Contains(string(c.raw), `"kind":"bounced"`) {
		t.Fatalf("contact after a hard bounce: %s", c.raw)
	}

	// A click goes through the redirect on the brand's link domain (the test server stands in).
	bob, _ := f.a.Store.InsertContact(ctx, store.NewContact{BrandID: f.brandID, Email: "bob@example.com", Status: store.ContactSubscribed,
		ConsentBasis: "double_opt_in", TrackingAllowed: true})
	send, err := f.a.Store.CreateSendForTest(ctx, f.brandID, bob)
	if err != nil {
		t.Fatal(err)
	}
	link, err := f.a.Tracking.ClickURL(f.srv, tracking.Link{BrandID: f.brandID, ContactID: bob, SendID: send, LinkNo: 1, LinkCount: 2, URL: "https://brightside.co/sale"})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", link, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) AppleWebKit/537.36 Chrome/129 Safari/537.36")
	res, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 302 || res.Header.Get("Location") != "https://brightside.co/sale" {
		t.Fatalf("click: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if got, _ := f.a.Store.ContactByID(ctx, f.brandID, bob); got.LastClickAt != nil {
		t.Fatal("a click counted as engagement before it settled")
	}
	if err := f.a.Store.SettleClicks(ctx, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.a.Store.ContactByID(ctx, f.brandID, bob); got.LastClickAt == nil {
		t.Fatal("a settled click not recorded as engagement")
	}

	// 7 days of feedback: 2.5% hard bounces → the brand brake slows it down,
	// the health score sees the rate, and S2 lists what the brake did.
	if err := f.a.Store.ExecForTest(ctx, `
		INSERT INTO email_events (event_id, type, subtype, brand_id, ses_message_id, email_hash, occurred_at)
		SELECT 'bulk-' || g, CASE WHEN g <= 25 THEN 'bounce' ELSE 'delivery' END, CASE WHEN g <= 25 THEN 'permanent/General' ELSE '' END,
			$1, 'bm-' || g, decode(md5(g::text), 'hex'), now() - interval '1 day'
		FROM generate_series(1, 1000) g`, f.brandID); err != nil {
		t.Fatal(err)
	}
	if err := f.a.Brakes.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	h := f.owner.must(200, f.owner.do("GET", "/admin/v1/brands/brightside/health", nil))
	if h.body["brand"].(map[string]any)["brake_state"] != "slowed" {
		t.Fatalf("brake: %s", h.raw)
	}
	evs := h.body["safety_events"].([]any)
	if len(evs) != 1 || evs[0].(map[string]any)["action"] != "slowed" {
		t.Fatalf("safety events: %s", h.raw)
	}
	recheck := f.owner.must(200, f.owner.do("POST", "/admin/v1/brands/brightside/health/recheck", nil))
	if !strings.Contains(string(recheck.raw), "Bounces (7 days)") {
		t.Fatalf("health score without the brand's own rates: %s", recheck.raw)
	}
	gate, err := f.a.Brakes.CheckGate(ctx, nil, f.brandID)
	if err != nil || !gate.Allowed || gate.RateFactor != 0.5 || !gate.NeedsApproval {
		t.Fatalf("gate: %+v %v", gate, err)
	}
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

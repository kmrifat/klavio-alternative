package app_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/events"
	"github.com/kmrifat/klavio-alternative/api/internal/ses"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

var clickRe = regexp.MustCompile(`https://go\.brightside\.co(/c/[A-Za-z0-9_-]+)`)

// confirmAll schedules a draft for its whole audience, as reviewed.
func (w *campaignWorld) confirmAll(id string) {
	w.t.Helper()
	rv := w.owner.must(200, w.owner.do("GET", campaignBase+"/campaigns/"+id+"/review", nil))
	w.owner.must(200, w.owner.do("POST", campaignBase+"/campaigns/"+id+"/schedule", map[string]any{"version": 1, "confirm": rv.body["confirm_text"]}))
}

func (w *campaignWorld) messagesTo(to string) []ses.Message {
	var out []ses.Message
	for _, m := range w.sender.Messages() {
		if m.To == to {
			out = append(out, m)
		}
	}
	return out
}

func TestCampaignTrackingAndReport(t *testing.T) {
	w := newCampaignWorld(t)
	ctx := context.Background()
	for i := range 4 {
		w.contact(fmt.Sprintf("reader%d@gmail.com", i), store.ContactSubscribed, 24*time.Hour, true)
	}
	// No tracking consent (EU): working links, no pixel, nothing recorded.
	private := w.contact("private@example.org", store.ContactSubscribed, 24*time.Hour, true)
	if err := w.a.Store.ExecForTest(ctx, `UPDATE contacts SET tracking_allowed = false WHERE id = $1`, private); err != nil {
		t.Fatal(err)
	}
	id := w.draft(nil)["id"].(string)
	w.confirmAll(id)
	w.runUntil(id, store.CampaignDone)

	m := w.messagesTo("reader0@gmail.com")[0]
	if strings.Contains(m.HTML, `href="https://shop.example.com/autumn"`) || !clickRe.MatchString(m.HTML) {
		t.Fatalf("links not tracked: %s", m.HTML)
	}
	if !strings.Contains(m.HTML, "https://go.brightside.co/u/") || !strings.Contains(m.HTML, "https://go.brightside.co/o/") {
		t.Fatalf("unsubscribe link must stay as it is, and the pixel be there: %s", m.HTML)
	}
	for _, l := range clickRe.FindAllStringSubmatch(m.HTML, -1) {
		if _, err := w.a.Tracking.Click(strings.TrimPrefix(l[1], "/c/")); err != nil {
			t.Fatalf("bad click token: %v", err)
		}
	}
	pm := w.messagesTo("private@example.org")[0]
	if strings.Contains(pm.HTML, "/o/") || !clickRe.MatchString(pm.HTML) {
		t.Fatalf("no-tracking recipient: pixel %v, links %v", strings.Contains(pm.HTML, "/o/"), clickRe.MatchString(pm.HTML))
	}

	// A click goes through the redirect (the test server stands in for go.brightside.co).
	srv := w.owner.base
	click := func(msg ses.Message) {
		t.Helper()
		path := clickRe.FindStringSubmatch(msg.HTML)[1]
		req, _ := http.NewRequest("GET", srv+path, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148")
		res, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 302 || res.Header.Get("Location") != "https://shop.example.com/autumn" {
			t.Fatalf("click: %d %s", res.StatusCode, res.Header.Get("Location"))
		}
	}
	click(m)
	click(pm)

	// SES says one email bounced: mapped to its send through the send_id tag.
	bounced := w.messagesTo("reader3@gmail.com")[0]
	q := &events.Fake{}
	q.Push([]byte(fmt.Sprintf(`{"id":"ev-b","source":"aws.ses","time":"2026-10-10T09:00:00Z","detail":{"eventType":"Bounce",
		"mail":{"messageId":"m-b","destination":["reader3@gmail.com"],"tags":{"ses:configuration-set":["brand-brightside"],"send_id":[%q],"campaign_id":[%q]}},
		"bounce":{"bounceType":"Permanent","bounceSubType":"General","bouncedRecipients":[{"emailAddress":"reader3@gmail.com"}]}}}`,
		bounced.Tags["send_id"], id)))
	for _, to := range []string{"reader0@gmail.com", "reader1@gmail.com", "reader2@gmail.com"} {
		msg := w.messagesTo(to)[0]
		q.Push([]byte(fmt.Sprintf(`{"id":"ev-d-%s","source":"aws.ses","time":"2026-10-10T09:00:00Z","detail":{"eventType":"Delivery",
			"mail":{"messageId":"m-%s","destination":[%q],"tags":{"ses:configuration-set":["brand-brightside"],"send_id":[%q]}},
			"delivery":{"recipients":[%q],"timestamp":"2026-10-10T09:00:01Z"}}}`, to, to, to, msg.Tags["send_id"], to)))
	}
	r := &events.Reader{Queue: q, Processor: w.a.Events, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if n, err := r.Once(ctx); err != nil || n != 4 {
		t.Fatalf("events: %d %v", n, err)
	}

	rep := w.owner.must(200, w.owner.do("GET", campaignBase+"/campaigns/"+id+"/report", nil))
	fb := rep.body["feedback"].(map[string]any)
	eng := rep.body["engagement"].(map[string]any)
	if num(fb["bounces"]) != 1 || num(fb["delivered"]) != 3 || num(eng["human_clicks"]) != 1 || num(eng["unique_clickers"]) != 1 {
		t.Fatalf("report: %s", rep.raw)
	}
	links := rep.body["links"].([]any)
	if len(links) != 1 || links[0].(map[string]any)["url"] != "https://shop.example.com/autumn" {
		t.Fatalf("links: %v", links)
	}
	prov := rep.body["providers"].([]any)[0].(map[string]any)
	if prov["provider"] != "Gmail" || num(prov["sent"]) != 4 || num(prov["bounces"]) != 1 || num(prov["clickers"]) != 1 {
		t.Fatalf("providers: %v", rep.body["providers"])
	}
	mon := w.owner.must(200, w.owner.do("GET", campaignBase+"/campaigns/"+id+"/monitor", nil))
	if num(mon.body["sends"].(map[string]any)["sent"]) != 5 || mon.body["gate"].(map[string]any)["allowed"] != true {
		t.Fatalf("monitor: %s", mon.raw)
	}
}

func TestCampaignBrakes(t *testing.T) {
	w := newCampaignWorld(t)
	ctx := context.Background()
	for i := range 40 {
		w.contact(fmt.Sprintf("p%02d@example.com", i), store.ContactSubscribed, 24*time.Hour, true)
	}

	// 1. After the test slice: its sends bounced too much → the campaign pauses.
	id := w.draft(nil)["id"].(string)
	w.confirmAll(id)
	w.runUntil(id, store.CampaignHolding)
	cid := uuid.MustParse(id)
	// 150 attempts on the test sends, 4% hard bounces.
	if err := w.a.Store.ExecForTest(ctx, `
		INSERT INTO email_events (event_id, type, subtype, brand_id, ses_message_id, send_id, email_hash, occurred_at)
		SELECT 'tb-' || g, CASE WHEN g <= 6 THEN 'bounce' ELSE 'delivery' END, CASE WHEN g <= 6 THEN 'permanent/General' ELSE '' END,
			$2, 'tm-' || g, (SELECT min(id) FROM sends WHERE campaign_id = $1), decode(md5(g::text), 'hex'), now()
		FROM generate_series(1, 150) g`, cid, w.brand.ID); err != nil {
		t.Fatal(err)
	}
	c := w.runUntil(id, store.CampaignPaused)
	if !strings.Contains(c.PauseReason, "campaign brake") {
		t.Fatalf("pause reason: %q", c.PauseReason)
	}
	evs, _ := w.a.Store.CampaignSafetyEvents(ctx, cid)
	if len(evs) != 1 || evs[0].Scope != "campaign" || evs[0].Action != "paused" {
		t.Fatalf("campaign safety events: %+v", evs)
	}

	// 2. The brand brake pauses a running campaign at the next batch.
	id2 := w.draft(map[string]any{"name": "Second"})["id"].(string)
	w.confirmAll(id2)
	w.runUntil(id2, store.CampaignHolding)
	if err := w.a.Store.ExecForTest(ctx, `UPDATE brands SET brake_state = 'slowed' WHERE id = $1`, w.brand.ID); err != nil {
		t.Fatal(err)
	}
	c2 := w.runUntil(id2, store.CampaignPaused)
	if !strings.Contains(c2.PauseReason, "second admin") {
		t.Fatalf("slowed pause: %q", c2.PauseReason)
	}
	if r := w.owner.do("POST", campaignBase+"/campaigns/"+id2+"/resume", map[string]any{}); r.errCode() != "brand_slowed" {
		t.Fatalf("resume while slowed: %d %s", r.status, r.raw)
	}
	// While slowed, a new send needs approval.
	id3 := w.draft(map[string]any{"name": "Third"})["id"].(string)
	rv := w.owner.must(200, w.owner.do("GET", campaignBase+"/campaigns/"+id3+"/review", nil))
	if !strings.Contains(string(rv.raw), `"brand_slowed"`) {
		t.Fatalf("review while slowed: %s", rv.raw)
	}

	// 3. On watch, people who went quiet since the snapshot are skipped.
	if err := w.a.Store.ExecForTest(ctx, `UPDATE brands SET brake_state = 'watch' WHERE id = $1`, w.brand.ID); err != nil {
		t.Fatal(err)
	}
	quiet := w.contact("quiet-now@example.com", store.ContactSubscribed, 24*time.Hour, true)
	id4 := w.draft(map[string]any{"name": "Fourth"})["id"].(string)
	w.confirmAll(id4)
	if err := w.a.Store.ExecForTest(ctx, `UPDATE contacts SET last_click_at = now() - interval '120 days', last_engaged_at = now() - interval '120 days' WHERE id = $1`, quiet); err != nil {
		t.Fatal(err)
	}
	w.runUntil(id4, store.CampaignDone)
	if len(w.messagesTo("quiet-now@example.com")) != 0 {
		t.Fatal("an at-risk contact was emailed while the brand was on watch")
	}
	rep := w.owner.must(200, w.owner.do("GET", campaignBase+"/campaigns/"+id4+"/monitor", nil))
	if num(rep.body["sends"].(map[string]any)["skipped"].(map[string]any)["brake_inactive"]) != 1 {
		t.Fatalf("skip reasons: %s", rep.raw)
	}
}

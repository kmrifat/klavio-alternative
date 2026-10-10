package app_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/app"
	"github.com/kmrifat/klavio-alternative/api/internal/ses"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// nopCampaignRunner drops queued steps; the test runs them itself.
type nopCampaignRunner struct{}

func (nopCampaignRunner) QueueCampaignStep(context.Context, uuid.UUID, time.Time) error { return nil }

type campaignWorld struct {
	t      *testing.T
	a      *app.App
	owner  *browser
	sender *ses.FakeSender
	brand  store.Brand
	topic  string
	tpl    string
}

const campaignBase = "/admin/v1/brands/brightside"

// newCampaignWorld: a ready brand, a template, and a topic with members in
// every state the audience rules care about.
func newCampaignWorld(t *testing.T) *campaignWorld {
	sender := &ses.FakeSender{}
	a, srv, dns := setupWith(t, func(d *app.Deps) {
		d.Sender = sender
		d.CampaignRunner = nopCampaignRunner{}
	})
	ctx := context.Background()
	owner := newBrowser(t, srv.URL)
	link, _ := a.Bootstrap(ctx, "owner@acme.test", "Olivia Owner")
	owner.must(200, owner.acceptInvite(link))
	created := owner.must(201, owner.do("POST", "/admin/v1/brands", brightside))
	var recs []map[string]any
	for _, r := range created.body["dns_records"].([]any) {
		recs = append(recs, r.(map[string]any))
	}
	dns.publish(recs)
	owner.must(200, owner.do("POST", campaignBase+"/dns-check", nil))
	b, err := a.Store.BrandBySlug(ctx, "brightside")
	if err != nil || b.Status != store.BrandReady {
		t.Fatalf("brand not ready: %v %v", b.Status, err)
	}

	topics := owner.must(200, owner.do("GET", campaignBase+"/topics", nil)).body["topics"].([]any)
	topic := topics[0].(map[string]any)["id"].(string)

	owner.must(201, owner.do("POST", campaignBase+"/link-allowlist", map[string]any{"domain": "shop.example.com"}))
	tpl := owner.must(201, owner.do("POST", campaignBase+"/templates", map[string]any{"name": "Autumn", "mjml": welcome}))
	return &campaignWorld{t: t, a: a, owner: owner, sender: sender, brand: b, topic: topic,
		tpl: tpl.body["template"].(map[string]any)["id"].(string)}
}

// contact adds a topic member.
func (w *campaignWorld) contact(email string, status store.ContactStatus, lastClick time.Duration, inTopic bool) uuid.UUID {
	w.t.Helper()
	ctx := context.Background()
	now := time.Now()
	consented := now.Add(-400 * 24 * time.Hour)
	var click *time.Time
	if lastClick > 0 {
		c := now.Add(-lastClick)
		click = &c
	}
	id, err := w.a.Store.InsertContact(ctx, store.NewContact{
		BrandID: w.brand.ID, Email: email, FirstName: strings.Split(email, "@")[0], Status: status,
		ConsentBasis: "single_opt_in", ConsentedAt: &consented, LastClickAt: click, TrackingAllowed: true,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	if inTopic {
		if err := w.a.Store.AddContactTopics(ctx, id, []uuid.UUID{uuid.MustParse(w.topic)}); err != nil {
			w.t.Fatal(err)
		}
	}
	return id
}

// runUntil steps a campaign (forcing waits to be over) until it reaches state.
func (w *campaignWorld) runUntil(id string, want store.CampaignState) store.Campaign {
	w.t.Helper()
	ctx := context.Background()
	cid := uuid.MustParse(id)
	for range 50 {
		c, err := w.a.Store.CampaignByID(ctx, w.brand.ID, cid)
		if err != nil {
			w.t.Fatal(err)
		}
		if c.State == want {
			return c
		}
		if err := w.a.Store.ForceCampaignDueForTest(ctx, cid); err != nil {
			w.t.Fatal(err)
		}
		if err := w.a.Campaigns.Step(ctx, cid); err != nil {
			w.t.Fatal(err)
		}
	}
	c, _ := w.a.Store.CampaignByID(ctx, w.brand.ID, cid)
	w.t.Fatalf("campaign stuck in %s, wanted %s", c.State, want)
	return c
}

func (w *campaignWorld) draft(extra map[string]any) map[string]any {
	in := map[string]any{"name": "Autumn launch", "topic_id": w.topic, "template_id": w.tpl,
		"subject": "Hi {{first_name | there}}, autumn is here", "preheader": "New jackets inside"}
	for k, v := range extra {
		in[k] = v
	}
	return w.owner.must(201, w.owner.do("POST", campaignBase+"/campaigns", in)).body["campaign"].(map[string]any)
}

func TestCampaignSendsEndToEnd(t *testing.T) {
	w := newCampaignWorld(t)
	ctx := context.Background()

	for i := range 17 {
		w.contact(fmt.Sprintf("reader%02d@example.com", i), store.ContactSubscribed, 24*time.Hour, true)
	}
	w.contact("reject@example.com", store.ContactSubscribed, 24*time.Hour, true)   // SES refuses it
	w.contact("throttle@example.com", store.ContactSubscribed, 24*time.Hour, true) // throttled once, then sent
	leaver := w.contact("leaver@example.com", store.ContactSubscribed, 24*time.Hour, true)
	w.contact("gone@example.com", store.ContactUnsubscribed, 24*time.Hour, true)
	w.contact("quiet@example.com", store.ContactQuarantined, 24*time.Hour, true)
	w.contact("waiting@example.com", store.ContactPending, 24*time.Hour, true)
	w.contact("faded@example.com", store.ContactSubscribed, 0, true)                   // never clicked, opted in 400 days ago
	w.contact("drifting@example.com", store.ContactSubscribed, 120*24*time.Hour, true) // at risk
	w.contact("other@example.com", store.ContactSubscribed, 24*time.Hour, false)       // not in the topic
	w.contact("bounced@example.com", store.ContactSubscribed, 24*time.Hour, true)
	if err := w.a.Store.Suppress(ctx, "bounced@example.com", store.SuppressionReason("hard_bounce"), nil, "", nil); err != nil {
		t.Fatal(err)
	}

	c := w.draft(nil)
	id := c["id"].(string)
	got := w.owner.must(200, w.owner.do("GET", campaignBase+"/campaigns/"+id, nil)).body["audience"].(map[string]any)
	excluded := got["excluded"].(map[string]any)
	if num(got["included"]) != 20 || num(excluded["unsubscribed"]) != 1 || num(excluded["quarantined"]) != 1 ||
		num(excluded["pending_confirmation"]) != 1 || num(excluded["sunset"]) != 1 || num(excluded["at_risk"]) != 1 ||
		num(excluded["suppressed"]) != 1 {
		t.Fatalf("audience: %v", got)
	}

	// Test send to the seed list.
	owner := w.owner
	if r := owner.do("POST", campaignBase+"/campaigns/"+id+"/test", nil); r.errCode() != "no_seeds" {
		t.Fatalf("test without seeds: %d %s", r.status, r.raw)
	}
	owner.must(200, owner.do("POST", campaignBase+"/seeds", map[string]any{"email": "Team@Brightside.example"}))
	owner.must(200, owner.do("POST", campaignBase+"/campaigns/"+id+"/test", nil))
	msgs := w.sender.Messages()
	if len(msgs) != 1 || msgs[0].To != "team@brightside.example" || !strings.HasPrefix(msgs[0].Subject, "[Test] Hi Alex") {
		t.Fatalf("test send: %+v", msgs)
	}

	// The confirmation screen (S6) and its typed number.
	rv := owner.must(200, owner.do("GET", campaignBase+"/campaigns/"+id+"/review", nil))
	if rv.body["confirm_text"] != "20" || len(rv.body["problems"].([]any)) != 0 || len(rv.body["approval_reasons"].([]any)) != 0 {
		t.Fatalf("review: %s", rv.raw)
	}
	if r := owner.do("POST", campaignBase+"/campaigns/"+id+"/schedule", map[string]any{"version": 1, "confirm": "21"}); r.errCode() != "confirm_mismatch" {
		t.Fatalf("wrong confirm: %d %s", r.status, r.raw)
	}
	sched := owner.must(200, owner.do("POST", campaignBase+"/campaigns/"+id+"/schedule", map[string]any{"version": 1, "confirm": "2 0"}))
	sc := sched.body["campaign"].(map[string]any)
	start, _ := time.Parse(time.RFC3339Nano, sc["start_at"].(string))
	if sc["state"] != "scheduled" || time.Until(start) < 9*time.Minute {
		t.Fatalf("scheduled: state %v, starts in %v", sc["state"], time.Until(start))
	}
	breakdown := sched.body["breakdown"].(map[string]any)
	if num(breakdown["included_test"]) != 1 || num(breakdown["included_rest"]) != 19 || num(breakdown["sunset"]) != 1 {
		t.Fatalf("snapshot: %v", breakdown)
	}
	if r := owner.do("PUT", campaignBase+"/campaigns/"+id, map[string]any{"name": "x", "topic_id": w.topic, "version": 1}); r.errCode() != "not_editable" {
		t.Fatalf("edit after scheduling: %d %s", r.status, r.raw)
	}

	// Inside the cancel window nothing goes out.
	if err := w.a.Campaigns.Step(ctx, uuid.MustParse(id)); err != nil {
		t.Fatal(err)
	}
	if n := len(w.sender.Messages()); n != 1 {
		t.Fatalf("sent %d during the cancel window", n-1)
	}

	// Test slice, then hold.
	w.runUntil(id, store.CampaignHolding)
	if n := len(w.sender.Messages()) - 1; n > 1 {
		t.Fatalf("test slice sent %d emails, want at most 1", n)
	}

	// Someone unsubscribes while the campaign is under way: skipped.
	if err := w.a.Store.SetContactStatus(ctx, leaver, store.ContactUnsubscribed); err != nil {
		t.Fatal(err)
	}
	done := w.runUntil(id, store.CampaignDone)

	progress, err := w.a.Store.SendProgress(ctx, done.ID)
	if err != nil {
		t.Fatal(err)
	}
	sent := progress["test"]["sent"] + progress["rest"]["sent"]
	failed := progress["test"]["failed"] + progress["rest"]["failed"]
	skipped := progress["test"]["skipped"] + progress["rest"]["skipped"]
	if sent != 18 || failed != 1 || skipped != 1 {
		t.Fatalf("progress %v: sent %d failed %d skipped %d", progress, sent, failed, skipped)
	}

	// Every real email: brand sender, one-click unsubscribe, personal links.
	seen := map[string]bool{}
	for _, m := range w.sender.Messages()[1:] {
		if seen[m.To] {
			t.Fatalf("%s got the campaign twice", m.To)
		}
		seen[m.To] = true
		if m.From != `"Brightside" <hello@news.brightside.co>` || m.ConfigurationSet != "brand-brightside" {
			t.Errorf("sender: %q %q", m.From, m.ConfigurationSet)
		}
		if !strings.HasPrefix(m.Headers["List-Unsubscribe"], "<https://go.brightside.co/u/") || m.Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
			t.Errorf("unsubscribe headers: %v", m.Headers)
		}
		if strings.Contains(m.HTML, "{{") || !strings.Contains(m.HTML, "https://go.brightside.co/u/") || !strings.Contains(m.HTML, "New jackets inside") {
			t.Errorf("%s: merge tags or preheader not filled", m.To)
		}
		if name := strings.Split(m.To, "@")[0]; m.Subject != "Hi "+name+", autumn is here" {
			t.Errorf("subject %q", m.Subject)
		}
		if m.Tags["campaign_id"] != id || m.Tags["send_id"] == "" {
			t.Errorf("tags %v", m.Tags)
		}
	}
	for _, never := range []string{"leaver@example.com", "gone@example.com", "bounced@example.com", "faded@example.com", "drifting@example.com", "other@example.com", "reject@example.com"} {
		if seen[never] {
			t.Errorf("%s should not have been sent the campaign", never)
		}
	}
	if !seen["throttle@example.com"] {
		t.Error("a throttled send should be retried and go out")
	}

	// Steps after the end do nothing: never twice.
	before := len(w.sender.Messages())
	for range 3 {
		_ = w.a.Store.ForceCampaignDueForTest(ctx, done.ID)
		_ = w.a.Campaigns.Step(ctx, done.ID)
		_ = w.a.Campaigns.Sweep(ctx)
	}
	if after := len(w.sender.Messages()); after != before {
		t.Fatalf("%d more emails after the campaign finished", after-before)
	}

	entries := owner.must(200, owner.do("GET", "/admin/v1/audit", nil)).body["entries"].([]any)
	actions := map[string]bool{}
	for _, e := range entries {
		actions[e.(map[string]any)["action"].(string)] = true
	}
	for _, want := range []string{"campaign.created", "campaign.scheduled", "campaign.test_sent", "seed.added"} {
		if !actions[want] {
			t.Errorf("audit log missing %s", want)
		}
	}
}

func TestCampaignGuards(t *testing.T) {
	w := newCampaignWorld(t)
	ctx := context.Background()
	for i := range 5 {
		w.contact(fmt.Sprintf("r%d@example.com", i), store.ContactSubscribed, 24*time.Hour, true)
	}
	w.contact("drifting@example.com", store.ContactSubscribed, 120*24*time.Hour, true)
	owner := w.owner

	// Cancel inside the window: nothing is ever sent.
	c := w.draft(nil)
	id := c["id"].(string)
	owner.must(200, owner.do("POST", campaignBase+"/campaigns/"+id+"/schedule", map[string]any{"version": 1, "confirm": "5"}))
	cancelled := owner.must(200, owner.do("POST", campaignBase+"/campaigns/"+id+"/cancel", nil))
	if cancelled.body["campaign"].(map[string]any)["state"] != "cancelled" {
		t.Fatalf("cancel: %s", cancelled.raw)
	}
	_ = w.a.Store.ForceCampaignDueForTest(ctx, uuid.MustParse(id))
	_ = w.a.Campaigns.Step(ctx, uuid.MustParse(id))
	if n := len(w.sender.Messages()); n != 0 {
		t.Fatalf("a cancelled campaign sent %d emails", n)
	}

	// At-risk contacts need a second person's approval, which isn't wired yet: refused.
	risky := w.draft(map[string]any{"name": "Win-back", "include_at_risk": true})
	rv := owner.must(200, owner.do("GET", campaignBase+"/campaigns/"+risky["id"].(string)+"/review", nil))
	reasons := rv.body["approval_reasons"].([]any)
	if len(reasons) != 1 || reasons[0].(map[string]any)["code"] != "at_risk_included" {
		t.Fatalf("approval reasons: %s", rv.raw)
	}
	if r := owner.do("POST", campaignBase+"/campaigns/"+risky["id"].(string)+"/schedule", map[string]any{"version": 1, "confirm": "6"}); r.errCode() != "approval_required" {
		t.Fatalf("risky send: %d %s", r.status, r.raw)
	}

	// Templates with errors, missing subjects and paused brands block scheduling.
	broken := owner.must(201, owner.do("POST", campaignBase+"/templates", map[string]any{"name": "Broken", "mjml": strings.Replace(welcome, "shop.example.com", "evil.example.net", 1)}))
	bad := w.draft(map[string]any{"name": "Bad", "subject": "", "template_id": broken.body["template"].(map[string]any)["id"]})
	r := owner.do("POST", campaignBase+"/campaigns/"+bad["id"].(string)+"/schedule", map[string]any{"version": 1, "confirm": "5"})
	if r.status != 422 || len(r.body["problems"].([]any)) != 2 {
		t.Fatalf("problems: %d %s", r.status, r.raw)
	}

	// Pause and resume while sending.
	run := w.draft(map[string]any{"name": "Run"})
	rid := run["id"].(string)
	owner.must(200, owner.do("POST", campaignBase+"/campaigns/"+rid+"/schedule", map[string]any{"version": 1, "confirm": "5"}))
	w.runUntil(rid, store.CampaignHolding)
	owner.must(200, owner.do("POST", campaignBase+"/campaigns/"+rid+"/pause", nil))
	sentBefore := len(w.sender.Messages())
	for range 3 {
		_ = w.a.Store.ForceCampaignDueForTest(ctx, uuid.MustParse(rid))
		_ = w.a.Campaigns.Step(ctx, uuid.MustParse(rid))
	}
	if len(w.sender.Messages()) != sentBefore {
		t.Fatal("a paused campaign kept sending")
	}
	owner.must(200, owner.do("POST", campaignBase+"/campaigns/"+rid+"/resume", nil))
	w.runUntil(rid, store.CampaignDone)
	if got := len(w.sender.Messages()); got != 5 {
		t.Fatalf("after resume: %d emails, want 5", got)
	}

	// Roles: an editor drafts and tests but can't schedule.
	inv := owner.must(201, owner.do("POST", "/admin/v1/team/invites", map[string]any{
		"email": "ed@acme.test", "name": "Ed", "company_role": "member", "brand_roles": map[string]string{w.brand.ID.String(): "editor"},
	}))
	editor := newBrowser(t, owner.base)
	editor.must(200, editor.acceptInvite(inv.body["invite_url"].(string)))
	draft := editor.must(201, editor.do("POST", campaignBase+"/campaigns", map[string]any{"name": "Ed's", "topic_id": w.topic, "template_id": w.tpl, "subject": "Hi"}))
	editor.must(403, editor.do("POST", campaignBase+"/campaigns/"+draft.body["campaign"].(map[string]any)["id"].(string)+"/schedule", map[string]any{"version": 1, "confirm": "5"}))
	editor.must(403, editor.do("POST", campaignBase+"/seeds", map[string]any{"email": "x@example.com"}))
	editor.must(403, editor.do("PUT", campaignBase+"/sender", map[string]any{"from_local": "news"}))

	// The sender can be changed by a brand admin, and the next send uses it.
	owner.must(200, owner.do("PUT", campaignBase+"/sender", map[string]any{"from_local": "news", "from_name": "Brightside Co", "reply_to": "help@brightside.co"}))
	rv = owner.must(200, owner.do("GET", campaignBase+"/campaigns/"+draft.body["campaign"].(map[string]any)["id"].(string)+"/review", nil))
	if rv.body["from_header"] != `"Brightside Co" <news@news.brightside.co>` {
		t.Fatalf("from header: %v", rv.body["from_header"])
	}
}

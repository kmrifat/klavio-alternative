package app_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

func attentionKinds(sc resp) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, x := range sc.body["needs_attention"].([]any) {
		m := x.(map[string]any)
		out[m["kind"].(string)] = m
	}
	return out
}

func component(t *testing.T, h map[string]any, key string) map[string]any {
	t.Helper()
	for _, c := range h["components"].([]any) {
		if m := c.(map[string]any); m["key"] == key {
			return m
		}
	}
	t.Fatalf("component %s missing", key)
	return nil
}

func TestHealthAndSafetyCenter(t *testing.T) {
	a, srv, dns := setup(t)
	ctx := context.Background()
	owner := newBrowser(t, srv.URL)
	link, _ := a.Bootstrap(ctx, "owner@acme.test", "Olivia Owner")
	owner.must(200, owner.acceptInvite(link))
	created := owner.must(201, owner.do("POST", "/admin/v1/brands", brightside))
	brandID := uuid.MustParse(created.body["brand"].(map[string]any)["id"].(string))

	// A new brand: DNS not published, so authentication fails and S1 says so.
	sc := owner.must(200, owner.do("GET", "/admin/v1/safety-center", nil))
	cards := sc.body["brands"].([]any)
	if len(cards) != 1 {
		t.Fatalf("want 1 brand card: %s", sc.raw)
	}
	card := cards[0].(map[string]any)
	if card["slug"] != "brightside" || card["level"] != "at_risk" {
		t.Fatalf("unpublished DNS should score at risk: %s", sc.raw)
	}
	kinds := attentionKinds(sc)
	if kinds["dns_broken"] == nil || kinds["dns_broken"]["severity"] != "critical" || kinds["dns_broken"]["link"] != "/settings/brands/brightside" {
		t.Fatalf("broken DNS should need attention: %s", sc.raw)
	}
	if kinds["postmaster_not_connected"] == nil {
		t.Fatalf("owners should be told Postmaster isn't connected: %s", sc.raw)
	}
	acc := sc.body["account"].(map[string]any)
	if acc["available"] != true || acc["max_24_hour_send"].(float64) != 50000 || acc["production_access"] != true {
		t.Fatalf("fake SES account meters: %s", sc.raw)
	}
	prot := map[string]bool{}
	for _, p := range sc.body["protections"].([]any) {
		m := p.(map[string]any)
		prot[m["key"].(string)] = m["on"].(bool)
	}
	if prot["two_person"] || !prot["blocklists"] || prot["postmaster"] || !prot["suppression"] {
		t.Fatalf("protections: %v", prot)
	}

	// Publish DNS and re-check: authentication passes, DMARC p=none is a nudge.
	var recs []map[string]any
	for _, r := range created.body["dns_records"].([]any) {
		recs = append(recs, r.(map[string]any))
	}
	dns.publish(recs)
	h := owner.must(200, owner.do("POST", "/admin/v1/brands/brightside/health/recheck", nil))
	health := h.body["health"].(map[string]any)
	auth := component(t, health, "authentication")
	if auth["score"].(float64) < 90 {
		t.Fatalf("published DNS should score well: %s", h.raw)
	}
	if h.body["brand"].(map[string]any)["status"] != "ready" {
		t.Fatalf("recheck should also run the DNS check: %s", h.raw)
	}
	// 2 domains + 2 registered domains, deduplicated (both under brightside.co) = 3, × 3 lists.
	if n := len(h.body["blocklists"].([]any)); n != 9 {
		t.Fatalf("want 9 blocklist results, got %d: %s", n, h.raw)
	}
	if component(t, health, "reputation")["score"].(float64) != 100 {
		t.Fatalf("clean blocklists: %s", h.raw)
	}
	if component(t, health, "engagement")["score"] != nil || component(t, health, "list_quality")["score"] != nil {
		t.Fatalf("no contacts → no list/engagement score: %s", h.raw)
	}
	if pm := h.body["postmaster"].(map[string]any); pm["status"] != "not_connected" {
		t.Fatalf("postmaster: %s", h.raw)
	}
	kinds = attentionKinds(owner.must(200, owner.do("GET", "/admin/v1/safety-center", nil)))
	if kinds["dns_broken"] != nil || kinds["dmarc_none"] == nil {
		t.Fatalf("after publishing: %v", kinds)
	}

	// A listing is cached: a recheck within the hour doesn't query again…
	dns.mu.Lock()
	dns.answers["brightside.co.dbl.spamhaus.org."] = []string{"127.0.1.2"}
	dns.mu.Unlock()
	h = owner.must(200, owner.do("POST", "/admin/v1/brands/brightside/health/recheck", nil))
	if component(t, h.body["health"].(map[string]any), "reputation")["score"].(float64) != 100 {
		t.Fatalf("blocklists re-queried within the hour: %s", h.raw)
	}
	// …but the periodic job (results older than the interval) does.
	if err := a.Store.ExecForTest(ctx, `UPDATE blocklist_checks SET claimed_at = now() - interval '7 hours', checked_at = now() - interval '7 hours'`); err != nil {
		t.Fatal(err)
	}
	if err := a.Health.CheckAllBlocklists(ctx); err != nil {
		t.Fatal(err)
	}
	h = owner.must(200, owner.do("GET", "/admin/v1/brands/brightside/health", nil))
	rep := component(t, h.body["health"].(map[string]any), "reputation")
	if rep["score"].(float64) != 0 {
		t.Fatalf("a listing should zero reputation: %s", h.raw)
	}
	sc = owner.must(200, owner.do("GET", "/admin/v1/safety-center", nil))
	if k := attentionKinds(sc)["blocklisted"]; k == nil || k["severity"] != "critical" {
		t.Fatalf("listing should be critical on S1: %s", sc.raw)
	}
	if first := sc.body["needs_attention"].([]any)[0].(map[string]any); first["severity"] != "critical" {
		t.Fatalf("critical items come first: %s", sc.raw)
	}

	// Warm-up shows on S1 once the brand sends.
	err := a.Store.InTx(ctx, func(tx *store.Store) error {
		_, err := a.Warmup.Reserve(ctx, tx, brandID, time.Now(), 150)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sc = owner.must(200, owner.do("GET", "/admin/v1/safety-center", nil))
	wu := sc.body["brands"].([]any)[0].(map[string]any)["warmup"].(map[string]any)
	if wu["day"].(float64) != 1 || wu["cap"].(float64) != 200 || wu["sent"].(float64) != 150 || wu["complete"] != false {
		t.Fatalf("warm-up: %v", wu)
	}
	if sc.body["account"].(map[string]any)["marketing_sent_today"].(float64) != 150 {
		t.Fatalf("today's sends: %s", sc.raw)
	}

	// S1 is company-level: brand members get 403, whatever their brand roles.
	// A member without a role on the brand doesn't see its health either.
	inv := owner.must(201, owner.do("POST", "/admin/v1/team/invites", map[string]any{
		"email": "mel@acme.test", "name": "Mel Member", "company_role": "member",
	}))
	mel := newBrowser(t, srv.URL)
	mel.must(200, mel.acceptInvite(inv.body["invite_url"].(string)))
	mel.must(403, mel.do("GET", "/admin/v1/safety-center", nil))
	mel.must(404, mel.do("GET", "/admin/v1/brands/brightside/health", nil))
	mel.must(404, mel.do("POST", "/admin/v1/brands/brightside/health/recheck", nil))

	// A viewer sees the brand's health but can't trigger outside lookups.
	inv = owner.must(201, owner.do("POST", "/admin/v1/team/invites", map[string]any{
		"email": "vic@acme.test", "name": "Vic Viewer", "company_role": "member",
		"brand_roles": map[string]any{brandID.String(): "viewer"},
	}))
	vic := newBrowser(t, srv.URL)
	vic.must(200, vic.acceptInvite(inv.body["invite_url"].(string)))
	vic.must(403, vic.do("GET", "/admin/v1/safety-center", nil))
	vic.must(200, vic.do("GET", "/admin/v1/brands/brightside/health", nil))
	vic.must(403, vic.do("POST", "/admin/v1/brands/brightside/health/recheck", nil))

	// Parallel re-checks claim the lookup once: 3 domains × 3 lists = 9 queries.
	if err := a.Store.ExecForTest(ctx, `UPDATE blocklist_checks SET claimed_at = now() - interval '2 hours', checked_at = now() - interval '2 hours'`); err != nil {
		t.Fatal(err)
	}
	dns.mu.Lock()
	dns.blocklistQueries = 0
	dns.mu.Unlock()
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() { owner.do("POST", "/admin/v1/brands/brightside/health/recheck", nil) })
	}
	wg.Wait()
	dns.mu.Lock()
	n := dns.blocklistQueries
	dns.mu.Unlock()
	if n != 9 {
		t.Fatalf("30 parallel re-checks made %d blocklist queries, want 9", n)
	}

	// The hourly job saves a score for every brand.
	if err := a.Store.ExecForTest(ctx, `DELETE FROM brand_health`); err != nil {
		t.Fatal(err)
	}
	if err := a.Health.RecomputeAll(ctx); err != nil {
		t.Fatal(err)
	}
	all, err := a.Store.AllHealth(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("saved scores: %v %v", all, err)
	}
}

func TestInviteForExistingAccount(t *testing.T) {
	a, srv, _ := setup(t)
	owner := newBrowser(t, srv.URL)
	link, _ := a.Bootstrap(context.Background(), "owner@acme.test", "Olivia Owner")
	owner.must(200, owner.acceptInvite(link))
	r := owner.do("POST", "/admin/v1/team/invites", map[string]any{"email": "Owner@Acme.test", "name": "Again", "company_role": "member"})
	if r.status != 409 || r.errCode() != "already_member" {
		t.Fatalf("invite for an existing account: %d %s", r.status, r.raw)
	}
}

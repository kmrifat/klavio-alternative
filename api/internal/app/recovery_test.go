package app_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

func TestBrandRecovery(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	// 4% hard bounces over the last day → paused.
	if err := f.a.Store.ExecForTest(ctx, `
		INSERT INTO email_events (event_id, type, subtype, brand_id, ses_message_id, email_hash, occurred_at)
		SELECT 'bulk-' || g, CASE WHEN g <= 40 THEN 'bounce' ELSE 'delivery' END, CASE WHEN g <= 40 THEN 'permanent/General' ELSE '' END,
			$1, 'bm-' || g, decode(md5(g::text), 'hex'), now() - interval '1 day'
		FROM generate_series(1, 1000) g`, f.brandID); err != nil {
		t.Fatal(err)
	}
	if err := f.a.Brakes.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	v := f.owner.must(200, f.owner.do("GET", "/admin/v1/brands/brightside/recovery", nil))
	if v.body["paused"] != true || v.body["still_over"] != true || v.body["event"] == nil || len(v.body["steps"].([]any)) != 5 {
		t.Fatalf("recovery view: %s", v.raw)
	}
	sc := f.owner.must(200, f.owner.do("GET", "/admin/v1/safety-center", nil))
	if k := attentionKinds(sc)["brand_paused"]; k == nil || k["link"] != "/b/brightside/health/recovery" {
		t.Fatalf("S1: %s", sc.raw)
	}

	all := []string{"cause", "list", "content", "auth", "plan"}
	body := map[string]any{"steps": all, "reason": "A 2019 import of old trade-show leads; removed it."}
	if r := f.owner.do("POST", "/admin/v1/brands/brightside/recovery/resume", map[string]any{"steps": all[:2], "reason": "fixed it all, honest"}); r.errCode() != "checklist_incomplete" {
		t.Fatalf("partial checklist: %d %s", r.status, r.raw)
	}
	if r := f.owner.do("POST", "/admin/v1/brands/brightside/recovery/resume", body); r.errCode() != "still_over_limit" {
		t.Fatalf("still over: %d %s", r.status, r.raw)
	}
	// The bounces age out of the 7-day window.
	if err := f.a.Store.ExecForTest(ctx, `UPDATE email_events SET occurred_at = now() - interval '8 days'`); err != nil {
		t.Fatal(err)
	}
	// One admin: nobody to check the request, so no resuming alone.
	if r := f.owner.do("POST", "/admin/v1/brands/brightside/recovery/resume", body); r.errCode() != "no_approver" {
		t.Fatalf("single admin: %d %s", r.status, r.raw)
	}
	inv := f.owner.must(201, f.owner.do("POST", "/admin/v1/team/invites", map[string]any{"email": "bo@acme.test", "name": "Bo", "company_role": "owner"}))
	bo := newBrowser(t, f.srv)
	bo.must(200, bo.acceptInvite(inv.body["invite_url"].(string)))

	req := f.owner.must(202, f.owner.do("POST", "/admin/v1/brands/brightside/recovery/resume", body))
	if r := f.owner.do("POST", "/admin/v1/brands/brightside/recovery/resume", body); r.errCode() != "already_requested" {
		t.Fatalf("second request: %d %s", r.status, r.raw)
	}
	id := req.body["approval"].(map[string]any)["id"].(string)
	f.owner.must(403, f.owner.do("POST", "/admin/v1/approvals/"+id+"/approve", map[string]any{}))
	bo.must(200, bo.do("POST", "/admin/v1/approvals/"+id+"/approve", map[string]any{}))

	b, _ := f.a.Store.BrandByID(ctx, f.brandID)
	if b.BrakeState != "watch" || b.Status != store.BrandReady {
		t.Fatalf("after recovery: brake %s, status %s", b.BrakeState, b.Status)
	}
	g, _ := f.a.Brakes.CheckGate(ctx, nil, f.brandID)
	if !g.Allowed || !g.BlockInactive {
		t.Fatalf("resumed on watch: %+v", g)
	}
	// Watch doesn't lift itself either: one more approved step to healthy.
	if err := f.a.Brakes.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	if b, _ := f.a.Store.BrandByID(ctx, f.brandID); b.BrakeState != "watch" {
		t.Fatalf("watch loosened on its own: %s", b.BrakeState)
	}
	req2 := f.owner.must(202, f.owner.do("POST", "/admin/v1/brands/brightside/recovery/resume", body))
	if !strings.Contains(req2.body["approval"].(map[string]any)["summary"].(string), "watch → healthy") {
		t.Fatalf("summary: %s", req2.raw)
	}
	bo.must(200, bo.do("POST", "/admin/v1/approvals/"+req2.body["approval"].(map[string]any)["id"].(string)+"/approve", map[string]any{}))
	if b, _ := f.a.Store.BrandByID(ctx, f.brandID); b.BrakeState != "healthy" || b.Status != store.BrandReady {
		t.Fatalf("after the second step: %s %s", b.BrakeState, b.Status)
	}
	if r := f.owner.do("POST", "/admin/v1/brands/brightside/recovery/resume", body); r.errCode() != "not_braked" {
		t.Fatalf("nothing to loosen: %d %s", r.status, r.raw)
	}

	// The account brake: stopped for everyone until an owner's request is approved.
	if err := f.a.Store.ExecForTest(ctx, `UPDATE company_state SET account_brake_at = now(), account_brake_reason = 'test'`); err != nil {
		t.Fatal(err)
	}
	if g, _ := f.a.Brakes.CheckGate(ctx, nil, f.brandID); g.Allowed {
		t.Fatal("account brake didn't stop the brand")
	}
	acc := f.owner.must(202, f.owner.do("POST", "/admin/v1/account-brake/release", map[string]any{
		"steps": []string{"cause", "brands", "amazon"}, "reason": "Complaints came from one brand; it's paused."}))
	bo.must(200, bo.do("POST", "/admin/v1/approvals/"+acc.body["approval"].(map[string]any)["id"].(string)+"/approve", map[string]any{}))
	if g, _ := f.a.Brakes.CheckGate(ctx, nil, f.brandID); !g.Allowed {
		t.Fatalf("after releasing the account brake: %+v", g)
	}
}

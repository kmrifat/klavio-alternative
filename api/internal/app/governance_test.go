package app_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/app"
	"github.com/kmrifat/klavio-alternative/api/internal/config"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// twoAdmins: owner A bootstraps; B is invited as a second owner (immediately,
// since A is the only admin) — from then on, admin changes need the other one.
type govFixture struct {
	a       *app.App
	srv     string
	ownerA  *browser
	ownerB  *browser
	brandID string
}

func newGov(t *testing.T) govFixture { return newGovWith(t, nil) }

func newGovWith(t *testing.T, edit func(*config.Config)) govFixture {
	a, srv, dns := setupWith(t, edit)
	_ = dns
	ctx := context.Background()
	ownerA := newBrowser(t, srv.URL)
	link, _ := a.Bootstrap(ctx, "alice@acme.test", "Alice Owner")
	ownerA.must(200, ownerA.acceptInvite(link))
	b := ownerA.must(201, ownerA.do("POST", "/admin/v1/brands", brightside)).body["brand"].(map[string]any)
	inv := ownerA.must(201, ownerA.do("POST", "/admin/v1/team/invites", map[string]any{
		"email": "bob@acme.test", "name": "Bob Owner", "company_role": "owner",
	}))
	if inv.body["awaiting_approval"] != false {
		t.Fatalf("first admin invite in a one-admin company should not wait: %s", inv.raw)
	}
	ownerB := newBrowser(t, srv.URL)
	ownerB.must(200, ownerB.acceptInvite(inv.body["invite_url"].(string)))
	return govFixture{a: a, srv: srv.URL, ownerA: ownerA, ownerB: ownerB, brandID: b["id"].(string)}
}

func limitValue(t *testing.T, b *browser, key string) float64 {
	t.Helper()
	for _, l := range b.must(200, b.do("GET", "/admin/v1/safety-limits", nil)).body["limits"].([]any) {
		m := l.(map[string]any)
		if m["key"] == key {
			return m["value"].(float64)
		}
	}
	t.Fatalf("limit %s not found", key)
	return 0
}

func TestSafetyLimitsAndApprovals(t *testing.T) {
	f := newGov(t)
	ctx := context.Background()

	// Tighten applies now; loosen waits for the other owner.
	res := f.ownerA.must(200, f.ownerA.do("PUT", "/admin/v1/safety-limits", map[string]any{
		"changes": map[string]float64{"campaign_pause_unsubscribe_pct": 0.8, "campaign_pause_bounce_pct": 2.5},
		"reason":  "Black Friday list is cleaner than usual",
	})).body
	if len(res["applied"].([]any)) != 1 || len(res["requested"].([]any)) != 1 {
		t.Fatalf("change result: %v", res)
	}
	if limitValue(t, f.ownerA, "campaign_pause_unsubscribe_pct") != 0.8 || limitValue(t, f.ownerA, "campaign_pause_bounce_pct") != 2 {
		t.Fatal("tighten should apply now and loosen should wait")
	}
	approvalID := res["requested"].([]any)[0].(map[string]any)["id"].(string)

	// Hard and locked limits.
	if r := f.ownerA.do("PUT", "/admin/v1/safety-limits", map[string]any{"changes": map[string]float64{"campaign_pause_bounce_pct": 3.5}}); r.errCode() != "past_hard_limit" {
		t.Fatalf("past hard limit: %d %s", r.status, r.raw)
	}
	if r := f.ownerA.do("PUT", "/admin/v1/safety-limits", map[string]any{"changes": map[string]float64{"campaign_pause_complaint_pct": 0.1}}); r.errCode() != "locked" {
		t.Fatalf("locked: %d %s", r.status, r.raw)
	}

	// Nobody approves their own request; it doesn't even show as waiting for them.
	if list := f.ownerA.must(200, f.ownerA.do("GET", "/admin/v1/approvals?view=decide", nil)).body["approvals"].([]any); len(list) != 0 {
		t.Fatalf("own request listed for decision: %v", list)
	}
	if r := f.ownerA.do("POST", "/admin/v1/approvals/"+approvalID+"/approve", map[string]any{}); r.errCode() != "own_request" {
		t.Fatalf("self-approval: %d %s", r.status, r.raw)
	}

	// Approving needs a fresh passkey confirmation.
	if _, err := f.a.Store.MarkSteppedUpForTest(ctx, "bob@acme.test"); err != nil {
		t.Fatal(err)
	}
	if r := f.ownerB.do("POST", "/admin/v1/approvals/"+approvalID+"/approve", map[string]any{}); r.errCode() != "step_up_required" {
		t.Fatalf("approve without step-up: %d %s", r.status, r.raw)
	}
	f.ownerB.must(200, f.ownerB.stepUp())
	waiting := f.ownerB.must(200, f.ownerB.do("GET", "/admin/v1/approvals?view=decide", nil)).body["approvals"].([]any)
	if len(waiting) != 1 || waiting[0].(map[string]any)["requester_name"] != "Alice Owner" {
		t.Fatalf("B's queue: %v", waiting)
	}
	done := f.ownerB.must(200, f.ownerB.do("POST", "/admin/v1/approvals/"+approvalID+"/approve", map[string]any{"note": "ok for this week"})).body
	if done["status"] != "approved" || limitValue(t, f.ownerA, "campaign_pause_bounce_pct") != 2.5 {
		t.Fatalf("after approval: %v", done)
	}
	if r := f.ownerB.do("POST", "/admin/v1/approvals/"+approvalID+"/approve", map[string]any{}); r.errCode() != "not_pending" {
		t.Fatalf("approve twice: %d", r.status)
	}

	// Reject needs a reason; cancel is for the requester; expired can't be approved.
	req := func() string {
		r := f.ownerA.must(200, f.ownerA.do("PUT", "/admin/v1/safety-limits", map[string]any{"changes": map[string]float64{"repermission_emails_per_day": 300}})).body
		return r["requested"].([]any)[0].(map[string]any)["id"].(string)
	}
	id := req()
	if r := f.ownerB.do("POST", "/admin/v1/approvals/"+id+"/reject", map[string]any{"note": " "}); r.errCode() != "note_required" {
		t.Fatalf("reject without note: %d", r.status)
	}
	f.ownerB.must(200, f.ownerB.do("POST", "/admin/v1/approvals/"+id+"/reject", map[string]any{"note": "not during warm-up"}))
	id = req()
	f.ownerB.must(404, f.ownerB.do("POST", "/admin/v1/approvals/"+id+"/cancel", nil))
	f.ownerA.must(204, f.ownerA.do("POST", "/admin/v1/approvals/"+id+"/cancel", nil))
	id = req()
	if err := f.a.Store.ExpireApprovalForTest(ctx, uuid.MustParse(id)); err != nil {
		t.Fatal(err)
	}
	if r := f.ownerB.do("POST", "/admin/v1/approvals/"+id+"/approve", map[string]any{}); r.errCode() != "not_pending" {
		t.Fatalf("approve expired: %d", r.status)
	}
	if limitValue(t, f.ownerA, "repermission_emails_per_day") != 200 {
		t.Fatal("a rejected, cancelled or expired loosening took effect")
	}

	// The audit log saw all of it.
	entries := f.ownerA.must(200, f.ownerA.do("GET", "/admin/v1/audit?action=approval.", nil)).body["entries"].([]any)
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.(map[string]any)["action"].(string)] = true
	}
	for _, w := range []string{"approval.requested", "approval.approved", "approval.rejected", "approval.cancelled"} {
		if !seen[w] {
			t.Errorf("audit missing %s", w)
		}
	}
}

func TestAdminChangesNeedApproval(t *testing.T) {
	f := newGov(t)

	// An editor promoted to brand admin: waits for the other owner.
	inv := f.ownerA.must(201, f.ownerA.do("POST", "/admin/v1/team/invites", map[string]any{
		"email": "carol@acme.test", "name": "Carol Editor", "brand_roles": map[string]string{f.brandID: "editor"},
	}))
	carol := newBrowser(t, f.srv)
	carolID := carol.must(200, carol.acceptInvite(inv.body["invite_url"].(string))).body["user"].(map[string]any)["id"].(string)
	pending := f.ownerA.must(202, f.ownerA.do("PUT", "/admin/v1/team/members/"+carolID+"/roles", map[string]any{
		"company_role": "member", "brand_roles": map[string]string{f.brandID: "brand_admin"},
	})).body
	if me := carol.must(200, carol.do("GET", "/admin/v1/me", nil)).body; me["brand_roles"].(map[string]any)[f.brandID] != "editor" {
		t.Fatal("promotion applied before approval")
	}
	f.ownerB.must(200, f.ownerB.do("POST", "/admin/v1/approvals/"+pending["approval"].(map[string]any)["id"].(string)+"/approve", map[string]any{}))
	if me := carol.must(200, carol.do("GET", "/admin/v1/me", nil)).body; me["brand_roles"].(map[string]any)[f.brandID] != "brand_admin" {
		t.Fatal("promotion not applied after approval")
	}
	// Taking rights away applies at once.
	f.ownerA.must(204, f.ownerA.do("PUT", "/admin/v1/team/members/"+carolID+"/roles", map[string]any{
		"company_role": "member", "brand_roles": map[string]string{f.brandID: "viewer"},
	}))

	// An approver invite: the link exists but can't be used until approved.
	inv = f.ownerA.must(201, f.ownerA.do("POST", "/admin/v1/team/invites", map[string]any{
		"email": "dan@acme.test", "name": "Dan Approver", "company_role": "approver",
	}))
	if inv.body["awaiting_approval"] != true {
		t.Fatalf("admin invite should wait: %s", inv.raw)
	}
	dan := newBrowser(t, f.srv)
	token := inv.body["invite_url"].(string)[strings.LastIndex(inv.body["invite_url"].(string), "/")+1:]
	if r := dan.do("GET", "/admin/v1/invites/"+token, nil); r.errCode() != "invite_awaiting_approval" {
		t.Fatalf("unapproved invite: %d %s", r.status, r.raw)
	}
	approvalID := inv.body["approval"].(map[string]any)["id"].(string)
	f.ownerB.must(200, f.ownerB.do("POST", "/admin/v1/approvals/"+approvalID+"/approve", map[string]any{}))
	if me := dan.must(200, dan.acceptInvite(inv.body["invite_url"].(string))).body; me["user"].(map[string]any)["company_role"] != "approver" {
		t.Fatalf("dan: %v", me)
	}

	// A rejected admin invite is dead and frees the email.
	inv = f.ownerA.must(201, f.ownerA.do("POST", "/admin/v1/team/invites", map[string]any{"email": "eve@acme.test", "name": "Eve", "company_role": "owner"}))
	f.ownerB.must(200, f.ownerB.do("POST", "/admin/v1/approvals/"+inv.body["approval"].(map[string]any)["id"].(string)+"/reject", map[string]any{"note": "not needed"}))
	token = inv.body["invite_url"].(string)[strings.LastIndex(inv.body["invite_url"].(string), "/")+1:]
	if r := newBrowser(t, f.srv).do("GET", "/admin/v1/invites/"+token, nil); r.status != 404 {
		t.Fatalf("rejected invite: %d", r.status)
	}
	f.ownerA.must(201, f.ownerA.do("POST", "/admin/v1/team/invites", map[string]any{"email": "eve@acme.test", "name": "Eve", "brand_roles": map[string]string{f.brandID: "viewer"}}))
}

func TestExports(t *testing.T) {
	f := newGov(t)
	ctx := context.Background()
	base := "/admin/v1/brands/brightside"
	topic := f.ownerA.must(200, f.ownerA.do("GET", base+"/topics", nil)).body["topics"].([]any)[0].(map[string]any)["id"].(string)
	for _, c := range []map[string]any{
		{"email": "maya@example.com", "first_name": "Maya"},
		{"email": "evil@example.com", "first_name": `=HYPERLINK("https://evil.example","click")`},
	} {
		c["topic_ids"] = []string{topic}
		c["consent"] = map[string]string{"basis": "double_opt_in", "source": "Signup form"}
		f.ownerA.must(201, f.ownerA.do("POST", base+"/contacts", c))
	}

	if r := f.ownerA.do("POST", base+"/exports", map[string]any{"filter": map[string]any{}}); r.errCode() != "reason_required" {
		t.Fatalf("no reason: %d", r.status)
	}
	exp := f.ownerA.must(202, f.ownerA.do("POST", base+"/exports", map[string]any{"filter": map[string]any{}, "reason": "Upload to the CRM"})).body["export"].(map[string]any)
	expID := exp["id"].(string)
	if exp["row_count"] != 2.0 {
		t.Fatalf("export: %v", exp)
	}
	if r := f.ownerA.do("GET", "/admin/v1/exports/"+expID+"/download", nil); r.status != http.StatusGone {
		t.Fatalf("download before approval: %d", r.status)
	}
	f.ownerB.must(200, f.ownerB.do("POST", "/admin/v1/approvals/"+exp["approval_id"].(string)+"/approve", map[string]any{}))

	mine := f.ownerA.must(200, f.ownerA.do("GET", "/admin/v1/exports", nil)).body["exports"].([]any)
	if len(mine) != 1 || mine[0].(map[string]any)["status"] != "ready" {
		t.Fatalf("my exports: %v", mine)
	}
	// Only the requester can download.
	f.ownerB.must(404, f.ownerB.do("GET", "/admin/v1/exports/"+expID+"/download", nil))

	req, _ := http.NewRequest("GET", f.srv+"/admin/v1/exports/"+expID+"/download", nil)
	res, err := f.ownerA.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	csvBytes, _ := io.ReadAll(res.Body)
	res.Body.Close()
	body := string(csvBytes)
	if res.StatusCode != 200 || !strings.HasPrefix(body, "# CONFIDENTIAL — Brightside contacts") ||
		!strings.Contains(body, "alice@acme.test") || !strings.Contains(body, expID) {
		t.Fatalf("export file (%d):\n%s", res.StatusCode, body)
	}
	if !strings.Contains(body, `"'=HYPERLINK(`) {
		t.Fatalf("formula not neutralised:\n%s", body)
	}
	if !strings.Contains(res.Header.Get("Content-Disposition"), "attachment") {
		t.Fatal("not served as a download")
	}

	// After 24 hours the link is dead.
	if err := f.a.Store.ExpireExportForTest(ctx, uuid.MustParse(expID)); err != nil {
		t.Fatal(err)
	}
	if r := f.ownerA.do("GET", "/admin/v1/exports/"+expID+"/download", nil); r.status != http.StatusGone {
		t.Fatalf("download after expiry: %d", r.status)
	}
	entries := f.ownerA.must(200, f.ownerA.do("GET", "/admin/v1/audit?action=export.", nil)).body["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("export.downloaded audit entries: %d", len(entries))
	}
}

func TestAnomalyFreezeAndAlerts(t *testing.T) {
	f := newGov(t)
	ctx := context.Background()

	// Carol joins from the US.
	inv := f.ownerA.must(201, f.ownerA.do("POST", "/admin/v1/team/invites", map[string]any{
		"email": "carol@acme.test", "name": "Carol", "brand_roles": map[string]string{f.brandID: "brand_admin"},
	}))
	if inv.body["awaiting_approval"] == true {
		f.ownerB.must(200, f.ownerB.do("POST", "/admin/v1/approvals/"+inv.body["approval"].(map[string]any)["id"].(string)+"/approve", map[string]any{}))
	}
	carol := newBrowser(t, f.srv)
	carol.country = "US"
	carolID := carol.must(200, carol.acceptInvite(inv.body["invite_url"].(string))).body["user"].(map[string]any)["id"].(string)
	carol.must(200, carol.signIn()) // same country: fine

	// A second browser signs in as Carol from another country → frozen.
	thief := newBrowser(t, f.srv)
	thief.key, thief.country = carol.key, "RU"
	if r := thief.signIn(); r.errCode() != "account_frozen" {
		t.Fatalf("new-country sign-in: %d %s", r.status, r.raw)
	}
	carol.must(401, carol.do("GET", "/admin/v1/me", nil)) // her sessions ended too
	if r := carol.signIn(); r.errCode() != "account_frozen" {
		t.Fatalf("frozen account signed in: %d", r.status)
	}
	alerts := f.ownerA.must(200, f.ownerA.do("GET", "/admin/v1/alerts?open=true", nil)).body["alerts"].([]any)
	if len(alerts) == 0 || alerts[0].(map[string]any)["kind"] != "new_country" || alerts[0].(map[string]any)["severity"] != "critical" {
		t.Fatalf("alerts: %v", alerts)
	}
	team := f.ownerA.must(200, f.ownerA.do("GET", "/admin/v1/team", nil)).body["members"].([]any)
	for _, m := range team {
		if mm := m.(map[string]any); mm["id"] == carolID && mm["frozen_at"] == nil {
			t.Fatal("team list doesn't show Carol as frozen")
		}
	}

	// An owner checks and unfreezes; the alert is acknowledged.
	f.ownerA.must(204, f.ownerA.do("POST", "/admin/v1/team/members/"+carolID+"/unfreeze", nil))
	f.ownerA.must(204, f.ownerA.do("POST", "/admin/v1/alerts/"+alerts[0].(map[string]any)["id"].(string)+"/acknowledge", nil))
	carol.must(200, carol.signIn())

	// Owners are never frozen (someone must be able to unfreeze) but are alerted.
	f.ownerB.country = "US"
	f.ownerB.must(200, f.ownerB.signIn())
	f.ownerB.country = "BR"
	f.ownerB.must(200, f.ownerB.signIn())

	// Mass export: the 3rd export request in a day freezes the requester.
	base := "/admin/v1/brands/brightside"
	topic := carol.must(200, carol.do("GET", base+"/topics", nil)).body["topics"].([]any)[0].(map[string]any)["id"].(string)
	carol.must(201, carol.do("POST", base+"/contacts", map[string]any{"email": "maya@example.com", "topic_ids": []string{topic},
		"consent": map[string]string{"basis": "double_opt_in", "source": "Signup form"}}))
	for i := 1; i <= 3; i++ {
		r := carol.must(202, carol.do("POST", base+"/exports", map[string]any{"filter": map[string]any{}, "reason": fmt.Sprintf("try %d", i)}))
		if frozen := r.body["requester_frozen"]; frozen != (i == 3) {
			t.Fatalf("export %d: frozen=%v", i, frozen)
		}
	}
	carol.must(401, carol.do("GET", "/admin/v1/me", nil))
	kinds := map[string]bool{}
	for _, a := range f.ownerA.must(200, f.ownerA.do("GET", "/admin/v1/alerts", nil)).body["alerts"].([]any) {
		kinds[a.(map[string]any)["kind"].(string)] = true
	}
	for _, k := range []string{"new_country", "mass_export"} {
		if !kinds[k] {
			t.Errorf("missing %s alert (have %v)", k, kinds)
		}
	}
	if u, _ := f.a.Store.UserByID(ctx, uuid.MustParse(carolID)); u.FrozenAt == nil {
		t.Fatal("Carol should be frozen after a mass export")
	}
}

func TestImportGateFollowsSafetyLimits(t *testing.T) {
	_, run, base := importSetup(t)
	_ = base
	// 1 bad address in 80 = 1.25%: passes the default 2% gate…
	var rows []string
	for i := range 79 {
		rows = append(rows, fmt.Sprintf("p%d@example.com,2026-10-01", i))
	}
	rows = append(rows, "not-an-email,2026-10-01")
	csv := "email,date\n" + strings.Join(rows, "\n")
	checked := run(csv, map[string]any{"tracking": "all"}, map[string]string{"email": "email", "consented_at": "date"})
	if !checked["check_result"].(map[string]any)["gate"].(map[string]any)["passed"].(bool) {
		t.Fatalf("1.25%% invalid should pass the default gate: %v", checked["check_result"])
	}
}

func TestImportGateTightened(t *testing.T) {
	owner, run, _ := importSetup(t)
	// …but not after an admin tightens the limit to 1%.
	owner.must(200, owner.do("PUT", "/admin/v1/safety-limits", map[string]any{"changes": map[string]float64{"import_invalid_pct": 1}}))
	var rows []string
	for i := range 79 {
		rows = append(rows, fmt.Sprintf("q%d@example.com,2026-10-01", i))
	}
	rows = append(rows, "not-an-email,2026-10-01")
	imp := owner.must(201, owner.upload("/admin/v1/brands/brightside/imports", "x.csv", "email,date\n"+strings.Join(rows, "\n"))).body["import"].(map[string]any)
	id := imp["id"].(string)
	topic := owner.must(200, owner.do("GET", "/admin/v1/brands/brightside/topics", nil)).body["topics"].([]any)[0].(map[string]any)["id"].(string)
	owner.must(200, owner.do("PUT", "/admin/v1/brands/brightside/imports/"+id, map[string]any{
		"mapping": map[string]string{"email": "email", "consented_at": "date"}, "topic_ids": []string{topic},
		"consent": map[string]any{"source": "website_form", "collected_period": "2026", "wording": "x", "legal_basis": "consent", "attested": true, "tracking": "all"},
	}))
	checked := owner.must(202, owner.do("POST", "/admin/v1/brands/brightside/imports/"+id+"/check", nil)).body["import"].(map[string]any)
	gate := checked["check_result"].(map[string]any)["gate"].(map[string]any)
	if gate["passed"].(bool) || gate["invalid_limit"] != 0.01 {
		t.Fatalf("tightened gate: %v", gate)
	}
	_ = run
}

func TestAuditFiltersAndPaging(t *testing.T) {
	f := newGov(t)
	for i := range 5 {
		f.ownerA.must(201, f.ownerA.do("POST", "/admin/v1/brands/brightside/topics", map[string]any{"name": fmt.Sprintf("Topic %d", i)}))
	}
	first := f.ownerA.must(200, f.ownerA.do("GET", "/admin/v1/audit?action=topic.&limit=3", nil)).body
	page1 := first["entries"].([]any)
	if len(page1) != 3 || first["next_before"].(float64) == 0 {
		t.Fatalf("page 1: %v", first)
	}
	second := f.ownerA.must(200, f.ownerA.do("GET", fmt.Sprintf("/admin/v1/audit?action=topic.&limit=3&before=%d", int64(first["next_before"].(float64))), nil)).body
	if n := len(second["entries"].([]any)); n != 2 {
		t.Fatalf("page 2: %d", n)
	}
	byBrand := f.ownerA.must(200, f.ownerA.do("GET", "/admin/v1/audit?brand=brightside", nil)).body["entries"].([]any)
	for _, e := range byBrand {
		if e.(map[string]any)["brand_id"] != f.brandID {
			t.Fatalf("brand filter leaked: %v", e)
		}
	}
	byActor := f.ownerA.must(200, f.ownerA.do("GET", "/admin/v1/audit?actor=bob", nil)).body["entries"].([]any)
	for _, e := range byActor {
		if !strings.HasPrefix(e.(map[string]any)["actor_email"].(string), "bob") {
			t.Fatalf("actor filter leaked: %v", e)
		}
	}
	if r := f.ownerA.do("GET", "/admin/v1/audit?since=yesterday", nil); r.errCode() != "bad_date" {
		t.Fatalf("bad date: %d", r.status)
	}
}

var _ = store.ApprovalPending

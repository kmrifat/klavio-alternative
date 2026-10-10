package app_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// carol joins with the given brand roles (approved by B if needed).
func (f govFixture) carol(t *testing.T, roles map[string]string) (*browser, string) {
	t.Helper()
	inv := f.ownerA.must(201, f.ownerA.do("POST", "/admin/v1/team/invites", map[string]any{
		"email": "carol@acme.test", "name": "Carol", "brand_roles": roles,
	}))
	if inv.body["awaiting_approval"] == true {
		f.ownerB.must(200, f.ownerB.do("POST", "/admin/v1/approvals/"+inv.body["approval"].(map[string]any)["id"].(string)+"/approve", map[string]any{}))
	}
	c := newBrowser(t, f.srv)
	id := c.must(200, c.acceptInvite(inv.body["invite_url"].(string))).body["user"].(map[string]any)["id"].(string)
	return c, id
}

// Review blocker: demoting the other admin must not reopen the one-admin shortcut.
func TestDemoteThenGrantStillNeedsApproval(t *testing.T) {
	f := newGov(t)
	_, carolID := f.carol(t, map[string]string{f.brandID: "editor"})
	bobID := f.ownerB.must(200, f.ownerB.do("GET", "/admin/v1/me", nil)).body["user"].(map[string]any)["id"].(string)

	// Demoting Bob would leave Alice alone → needs approval (Bob's).
	r := f.ownerA.must(202, f.ownerA.do("PUT", "/admin/v1/team/members/"+bobID+"/roles", map[string]any{"company_role": "member"}))
	f.ownerB.must(200, f.ownerB.do("POST", "/admin/v1/approvals/"+r.body["approval"].(map[string]any)["id"].(string)+"/approve", map[string]any{}))

	// Now Alice is the only admin — but the company has had two, so no shortcut.
	if r := f.ownerA.do("PUT", "/admin/v1/team/members/"+carolID+"/roles", map[string]any{"company_role": "owner"}); r.errCode() != "no_approver" {
		t.Fatalf("grant after demotion: %d %s", r.status, r.raw)
	}
	if r := f.ownerA.do("POST", "/admin/v1/team/invites", map[string]any{"email": "mallory@acme.test", "name": "M", "company_role": "owner"}); r.errCode() != "no_approver" {
		t.Fatalf("admin invite after demotion: %d %s", r.status, r.raw)
	}
	me := newBrowser(t, f.srv)
	_ = me
	if u, _ := f.a.Store.UserByID(context.Background(), uuid.MustParse(carolID)); u.CompanyRole != store.CompanyMember {
		t.Fatalf("carol became %s", u.CompanyRole)
	}
}

func TestFrozenApproverDoesNotOpenShortcut(t *testing.T) {
	f := newGov(t)
	ctx := context.Background()
	_, carolID := f.carol(t, map[string]string{f.brandID: "editor"})
	bobID := uuid.MustParse(f.ownerB.must(200, f.ownerB.do("GET", "/admin/v1/me", nil)).body["user"].(map[string]any)["id"].(string))
	if err := f.a.Store.Freeze(ctx, bobID, "test"); err != nil {
		t.Fatal(err)
	}
	if r := f.ownerA.do("PUT", "/admin/v1/team/members/"+carolID+"/roles", map[string]any{"company_role": "owner"}); r.errCode() != "no_approver" {
		t.Fatalf("grant while the other admin is frozen: %d %s", r.status, r.raw)
	}
	f.ownerA.must(204, f.ownerA.do("POST", "/admin/v1/team/members/"+bobID.String()+"/unfreeze", nil))
	f.ownerA.must(202, f.ownerA.do("PUT", "/admin/v1/team/members/"+carolID+"/roles", map[string]any{"company_role": "owner"}))
}

// Review 2: approving an old loosening must not undo a later tightening.
func TestStaleLoosenIsRejected(t *testing.T) {
	f := newGov(t)
	req := f.ownerA.must(200, f.ownerA.do("PUT", "/admin/v1/safety-limits", map[string]any{"changes": map[string]float64{"campaign_pause_bounce_pct": 2.5}})).body
	id := req["requested"].([]any)[0].(map[string]any)["id"].(string)
	f.ownerB.must(200, f.ownerB.do("PUT", "/admin/v1/safety-limits", map[string]any{"changes": map[string]float64{"campaign_pause_bounce_pct": 1.5}}))
	res := f.ownerB.must(200, f.ownerB.do("POST", "/admin/v1/approvals/"+id+"/approve", map[string]any{})).body
	if res["status"] != "rejected" || limitValue(t, f.ownerA, "campaign_pause_bounce_pct") != 1.5 {
		t.Fatalf("stale loosen: %v, value %v", res, limitValue(t, f.ownerA, "campaign_pause_bounce_pct"))
	}
}

func TestLoosenRejectedWhenRequesterFrozen(t *testing.T) {
	f := newGov(t)
	ctx := context.Background()
	req := f.ownerA.must(200, f.ownerA.do("PUT", "/admin/v1/safety-limits", map[string]any{"changes": map[string]float64{"campaign_pause_bounce_pct": 2.5}})).body
	id := req["requested"].([]any)[0].(map[string]any)["id"].(string)
	aliceID := uuid.MustParse(req["requested"].([]any)[0].(map[string]any)["requested_by"].(string))
	if err := f.a.Store.Freeze(ctx, aliceID, "test"); err != nil {
		t.Fatal(err)
	}
	if res := f.ownerB.must(200, f.ownerB.do("POST", "/admin/v1/approvals/"+id+"/approve", map[string]any{})).body; res["status"] != "rejected" {
		t.Fatalf("frozen requester: %v", res)
	}
}

// Review 3: rights are checked again when the export runs and when it's downloaded.
func TestExportRequesterLosesRights(t *testing.T) {
	f := newGov(t)
	carol, carolID := f.carol(t, map[string]string{f.brandID: "brand_admin"})
	base := "/admin/v1/brands/brightside"
	topic := carol.must(200, carol.do("GET", base+"/topics", nil)).body["topics"].([]any)[0].(map[string]any)["id"].(string)
	carol.must(201, carol.do("POST", base+"/contacts", map[string]any{"email": "maya@example.com", "topic_ids": []string{topic},
		"consent": map[string]string{"basis": "double_opt_in", "source": "Signup form"}}))

	approved := carol.must(202, carol.do("POST", base+"/exports", map[string]any{"filter": map[string]any{}, "reason": "CRM"})).body["export"].(map[string]any)
	f.ownerB.must(200, f.ownerB.do("POST", "/admin/v1/approvals/"+approved["approval_id"].(string)+"/approve", map[string]any{}))
	pending := carol.must(202, carol.do("POST", base+"/exports", map[string]any{"filter": map[string]any{}, "reason": "CRM again"})).body["export"].(map[string]any)

	// Carol is moved to viewer.
	f.ownerA.must(204, f.ownerA.do("PUT", "/admin/v1/team/members/"+carolID+"/roles", map[string]any{
		"company_role": "member", "brand_roles": map[string]string{f.brandID: "viewer"},
	}))
	if r := carol.do("GET", "/admin/v1/exports/"+approved["id"].(string)+"/download", nil); r.errCode() != "export_forbidden" {
		t.Fatalf("download after losing rights: %d %s", r.status, r.raw)
	}
	res := f.ownerB.must(200, f.ownerB.do("POST", "/admin/v1/approvals/"+pending["approval_id"].(string)+"/approve", map[string]any{})).body
	if res["status"] != "rejected" {
		t.Fatalf("approving for someone without rights: %v", res)
	}
}

// Review 4: concurrent export requests can't dodge the "3rd in a day" freeze,
// and the size is re-checked when the export actually runs.
func TestExportFreezeIsNotRaceable(t *testing.T) {
	f := newGov(t)
	carol, carolID := f.carol(t, map[string]string{f.brandID: "brand_admin"})
	ctx := context.Background()
	base := "/admin/v1/brands/brightside"
	topic := carol.must(200, carol.do("GET", base+"/topics", nil)).body["topics"].([]any)[0].(map[string]any)["id"].(string)
	carol.must(201, carol.do("POST", base+"/contacts", map[string]any{"email": "maya@example.com", "topic_ids": []string{topic},
		"consent": map[string]string{"basis": "double_opt_in", "source": "Signup form"}}))

	var wg sync.WaitGroup
	results := make([]resp, 6)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = carol.do("POST", base+"/exports", map[string]any{"filter": map[string]any{}, "reason": fmt.Sprintf("x%d", i)})
		}()
	}
	wg.Wait()
	frozenSeen := false
	for _, r := range results {
		if r.status == 202 && r.body["requester_frozen"] == true {
			frozenSeen = true
		}
	}
	if u, _ := f.a.Store.UserByID(ctx, uuid.MustParse(carolID)); !frozenSeen || u.FrozenAt == nil {
		t.Fatalf("6 concurrent export requests didn't freeze the requester (seen=%v)", frozenSeen)
	}
	if list, _ := f.a.Store.ExportsBy(ctx, uuid.MustParse(carolID)); len(list) != 3 {
		t.Fatalf("%d export requests were created; requests after the freeze must be refused", len(list))
	}
}

func TestExportRecountedAtApproval(t *testing.T) {
	f := newGov(t)
	carol, carolID := f.carol(t, map[string]string{f.brandID: "brand_admin"})
	ctx := context.Background()
	base := "/admin/v1/brands/brightside"
	topic := carol.must(200, carol.do("GET", base+"/topics", nil)).body["topics"].([]any)[0].(map[string]any)["id"].(string)
	carol.must(201, carol.do("POST", base+"/contacts", map[string]any{"email": "maya@example.com", "topic_ids": []string{topic},
		"consent": map[string]string{"basis": "double_opt_in", "source": "Signup form"}}))
	exp := carol.must(202, carol.do("POST", base+"/exports", map[string]any{"filter": map[string]any{}, "reason": "small"})).body["export"].(map[string]any)

	// The list grows past the mass-export threshold before anyone approves.
	if err := f.a.Store.BulkInsertContactsForTest(ctx, uuid.MustParse(f.brandID), 5001); err != nil {
		t.Fatal(err)
	}
	waiting := f.ownerB.must(200, f.ownerB.do("GET", "/admin/v1/approvals?view=decide", nil)).body["approvals"].([]any)
	if len(waiting) == 0 || waiting[0].(map[string]any)["detail"] != "5002 contacts match these filters now (1 when requested)" {
		t.Fatalf("approver should see the live count: %v", waiting)
	}
	res := f.ownerB.must(200, f.ownerB.do("POST", "/admin/v1/approvals/"+exp["approval_id"].(string)+"/approve", map[string]any{})).body
	if res["status"] != "rejected" {
		t.Fatalf("an export that grew 5000× was approved as is: %v", res)
	}
	if u, _ := f.a.Store.UserByID(ctx, uuid.MustParse(carolID)); u.FrozenAt != nil {
		t.Fatal("an approver's decision shouldn't freeze the requester")
	}
}

// Review 5: concurrent limit saves can't produce an unapproved loosening.
func TestConcurrentLimitChanges(t *testing.T) {
	for round := range 5 {
		f := newGov(t)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			f.ownerA.do("PUT", "/admin/v1/safety-limits", map[string]any{"changes": map[string]float64{"campaign_pause_bounce_pct": 1}})
		}()
		go func() {
			defer wg.Done()
			f.ownerB.do("PUT", "/admin/v1/safety-limits", map[string]any{"changes": map[string]float64{"campaign_pause_bounce_pct": 1.9}})
		}()
		wg.Wait()
		if v := limitValue(t, f.ownerA, "campaign_pause_bounce_pct"); v != 1 {
			t.Fatalf("round %d: value %v; 1.9 after 1 would be an unapproved loosening", round, v)
		}
	}
}

// Nit: expiring an admin invite frees the email.
func TestExpiredAdminInviteFreesEmail(t *testing.T) {
	f := newGov(t)
	ctx := context.Background()
	inv := f.ownerA.must(201, f.ownerA.do("POST", "/admin/v1/team/invites", map[string]any{"email": "dan@acme.test", "name": "Dan", "company_role": "approver"}))
	id := uuid.MustParse(inv.body["approval"].(map[string]any)["id"].(string))
	if err := f.a.Store.ExpireApprovalForTest(ctx, id); err != nil {
		t.Fatal(err)
	}
	if n, err := f.a.Approvals.Expire(ctx); err != nil || n != 1 {
		t.Fatalf("expire: %d %v", n, err)
	}
	f.ownerA.must(201, f.ownerA.do("POST", "/admin/v1/team/invites", map[string]any{"email": "dan@acme.test", "name": "Dan", "company_role": "approver"}))
}

// Nit: a sign-in without a country, after ones with a country, is flagged.
func TestSignInWithoutCountryAlerts(t *testing.T) {
	f := newGov(t)
	carol, _ := f.carol(t, map[string]string{f.brandID: "editor"})
	carol.country = "US"
	carol.must(200, carol.signIn())
	carol.country = ""
	carol.must(200, carol.signIn())
	for _, a := range f.ownerA.must(200, f.ownerA.do("GET", "/admin/v1/alerts", nil)).body["alerts"].([]any) {
		if a.(map[string]any)["kind"] == "unknown_country" {
			return
		}
	}
	t.Fatal("no unknown_country alert")
}

// Re-probe 1: approving a request about yourself is self-approval in substance.
func TestCantDecideRequestAboutYourself(t *testing.T) {
	f := newGov(t)
	ctx := context.Background()
	inv := f.ownerA.must(201, f.ownerA.do("POST", "/admin/v1/team/invites", map[string]any{"email": "cara@acme.test", "name": "Cara Approver", "company_role": "approver"}))
	f.ownerB.must(200, f.ownerB.do("POST", "/admin/v1/approvals/"+inv.body["approval"].(map[string]any)["id"].(string)+"/approve", map[string]any{}))
	cara := newBrowser(t, f.srv)
	caraID := cara.must(200, cara.acceptInvite(inv.body["invite_url"].(string))).body["user"].(map[string]any)["id"].(string)

	req := f.ownerA.must(202, f.ownerA.do("PUT", "/admin/v1/team/members/"+caraID+"/roles", map[string]any{"company_role": "owner"})).body
	id := req["approval"].(map[string]any)["id"].(string)
	if list := cara.must(200, cara.do("GET", "/admin/v1/approvals?view=decide", nil)).body["approvals"].([]any); len(list) != 0 {
		t.Fatalf("her own promotion is in her queue: %v", list)
	}
	if r := cara.do("POST", "/admin/v1/approvals/"+id+"/approve", map[string]any{}); r.errCode() != "about_you" {
		t.Fatalf("self-promotion approval: %d %s", r.status, r.raw)
	}
	// The database refuses it too, whatever the code does.
	if err := f.a.Store.DecideApproval(ctx, uuid.MustParse(id), store.ApprovalApproved, ptr(uuid.MustParse(caraID)), "", nil); err == nil {
		t.Fatal("database accepted a decision by the request's subject")
	}
	f.ownerB.must(200, f.ownerB.do("POST", "/admin/v1/approvals/"+id+"/approve", map[string]any{}))

	// Admin invite for an address that already has an account: refused up
	// front, so no approval exists for its owner to decide.
	if r := f.ownerB.do("POST", "/admin/v1/team/invites", map[string]any{"email": "alice@acme.test", "name": "Alice again", "company_role": "approver"}); r.status != 409 || r.errCode() != "already_member" {
		t.Fatalf("admin invite for an existing account: %d %s", r.status, r.raw)
	}
	if list := f.ownerA.must(200, f.ownerA.do("GET", "/admin/v1/approvals?view=decide", nil)).body["approvals"].([]any); len(list) != 0 {
		t.Fatalf("refused invite left an approval behind: %v", list)
	}
}

func ptr[T any](v T) *T { return &v }

// Re-probe 2: owner invites made during the one-admin phase die once a second admin exists.
func TestShortcutEraAdminInviteStopsWorking(t *testing.T) {
	a, srv, _ := setup(t)
	alice := newBrowser(t, srv.URL)
	link, _ := a.Bootstrap(context.Background(), "alice@acme.test", "Alice")
	alice.must(200, alice.acceptInvite(link))
	bob := alice.must(201, alice.do("POST", "/admin/v1/team/invites", map[string]any{"email": "bob@acme.test", "name": "Bob", "company_role": "owner"}))
	alias := alice.must(201, alice.do("POST", "/admin/v1/team/invites", map[string]any{"email": "alias@acme.test", "name": "Alias", "company_role": "owner"}))
	if bob.body["awaiting_approval"] != false || alias.body["awaiting_approval"] != false {
		t.Fatal("one-admin invites should not wait")
	}
	b := newBrowser(t, srv.URL)
	b.must(200, b.acceptInvite(bob.body["invite_url"].(string)))

	token := alias.body["invite_url"].(string)[strings.LastIndex(alias.body["invite_url"].(string), "/")+1:]
	if r := newBrowser(t, srv.URL).do("GET", "/admin/v1/invites/"+token, nil); r.errCode() != "invite_needs_approval" {
		t.Fatalf("shortcut-era owner invite after two admins: %d %s", r.status, r.raw)
	}
}

// Re-probe 3: a large export an approver agreed to can complete.
func TestApprovedLargeExportCompletes(t *testing.T) {
	f := newGov(t)
	ctx := context.Background()
	if err := f.a.Store.BulkInsertContactsForTest(ctx, uuid.MustParse(f.brandID), 6001); err != nil {
		t.Fatal(err)
	}
	r := f.ownerA.must(202, f.ownerA.do("POST", "/admin/v1/brands/brightside/exports", map[string]any{"filter": map[string]any{}, "reason": "annual archive"})).body
	if r["requester_frozen"] != false {
		t.Fatal("owners are never frozen")
	}
	exp := r["export"].(map[string]any)
	res := f.ownerB.must(200, f.ownerB.do("POST", "/admin/v1/approvals/"+exp["approval_id"].(string)+"/approve", map[string]any{})).body
	if res["status"] != "approved" {
		t.Fatalf("approved large export: %v", res)
	}
	mine := f.ownerA.must(200, f.ownerA.do("GET", "/admin/v1/exports", nil)).body["exports"].([]any)
	if mine[0].(map[string]any)["status"] != "ready" || mine[0].(map[string]any)["row_count"] != 6001.0 {
		t.Fatalf("export: %v", mine[0])
	}
	kinds := map[string]bool{}
	for _, a := range f.ownerA.must(200, f.ownerA.do("GET", "/admin/v1/alerts", nil)).body["alerts"].([]any) {
		kinds[a.(map[string]any)["kind"].(string)] = true
	}
	if !kinds["mass_export"] {
		t.Fatal("a mass export should still raise an alert")
	}
}

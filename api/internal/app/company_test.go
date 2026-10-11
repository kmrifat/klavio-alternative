package app_test

import (
	"context"
	"strings"
	"testing"
)

func TestCompanyName(t *testing.T) {
	a, srv, _ := setup(t)
	ctx := context.Background()
	anon := newBrowser(t, srv.URL)
	owner := newBrowser(t, srv.URL)
	link, _ := a.Bootstrap(ctx, "owner@acme.test", "Olivia Owner")
	owner.must(200, owner.acceptInvite(link))

	// Until it's set, the passkey name (WEBAUTHN_RP_NAME) stands in, even
	// before anyone signs in.
	if m := anon.must(200, anon.do("GET", "/admin/v1/meta", nil)); m.body["company_name"] != "Test" {
		t.Fatalf("meta before: %s", m.raw)
	}
	c := owner.must(200, owner.do("GET", "/admin/v1/company", nil))
	inst := c.body["installation"].(map[string]any)
	if c.body["name"] != "" || c.body["display_name"] != "Test" || inst["passkey_domain"] != "localhost" || inst["env"] != "development" ||
		inst["cloudflare_access"] != false {
		t.Fatalf("company: %s", c.raw)
	}
	anon.must(401, anon.do("GET", "/admin/v1/company", nil))

	// Owners rename it, with a fresh passkey confirmation.
	if _, err := a.Store.MarkSteppedUpForTest(ctx, "owner@acme.test"); err != nil {
		t.Fatal(err)
	}
	if r := owner.do("PUT", "/admin/v1/company", map[string]any{"name": "Acme Group"}); r.errCode() != "step_up_required" {
		t.Fatalf("want step_up_required: %s", r.raw)
	}
	owner.must(200, owner.stepUp())
	for code, name := range map[string]string{"name_required": "   ", "name_too_long": strings.Repeat("x", 81)} {
		if r := owner.do("PUT", "/admin/v1/company", map[string]any{"name": name}); r.status != 400 || r.errCode() != code {
			t.Errorf("%s: %s", code, r.raw)
		}
	}
	c = owner.must(200, owner.do("PUT", "/admin/v1/company", map[string]any{"name": "  Acme   Group "}))
	if c.body["name"] != "Acme Group" || c.body["display_name"] != "Acme Group" {
		t.Fatalf("renamed: %s", c.raw)
	}
	if m := anon.must(200, anon.do("GET", "/admin/v1/meta", nil)); m.body["company_name"] != "Acme Group" {
		t.Fatalf("meta after: %s", m.raw)
	}
	var n int
	if err := a.Store.QueryRowForTest(ctx, `SELECT count(*) FROM audit_log WHERE action = 'company.renamed'
		AND before->>'name' = '' AND after->>'name' = 'Acme Group'`, &n); err != nil || n != 1 {
		t.Fatalf("audit: %d %v", n, err)
	}

	// Approvers and members can see it but not change it.
	inv := owner.must(201, owner.do("POST", "/admin/v1/team/invites", map[string]any{
		"email": "mel@acme.test", "name": "Mel Member", "company_role": "member",
	}))
	mel := newBrowser(t, srv.URL)
	mel.must(200, mel.acceptInvite(inv.body["invite_url"].(string)))
	mel.must(200, mel.do("GET", "/admin/v1/company", nil))
	mel.must(403, mel.do("PUT", "/admin/v1/company", map[string]any{"name": "Mine now"}))
}

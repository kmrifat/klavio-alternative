package app_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

const welcome = `<mj-section><mj-column>
  <mj-image src="https://brightside.co/hero.png" alt="Autumn collection" />
  <mj-text>Hi {{first_name | there}}, the autumn range is here. Every piece is made to last,
    shipped in recycled packaging, and members get early access all week long.</mj-text>
  <mj-button href="https://shop.example.com/autumn">Shop the range</mj-button>
</mj-column></mj-section>`

// With a second admin, a layout change waits for their approval.
func TestLayoutChangeNeedsSecondAdmin(t *testing.T) {
	a, srv, _ := setup(t)
	ctx := context.Background()
	owner := newBrowser(t, srv.URL)
	link, _ := a.Bootstrap(ctx, "owner@acme.test", "Olivia Owner")
	owner.must(200, owner.acceptInvite(link))
	owner.must(201, owner.do("POST", "/admin/v1/brands", brightside))
	const base = "/admin/v1/brands/brightside"
	tpl := owner.must(201, owner.do("POST", base+"/templates", map[string]any{"name": "Note", "mjml": welcome})).body["template"].(map[string]any)

	inv := owner.must(201, owner.do("POST", "/admin/v1/team/invites", map[string]any{
		"email": "ap@acme.test", "name": "Ada Approver", "company_role": "approver",
	}))
	approver := newBrowser(t, srv.URL)
	approver.must(200, approver.acceptInvite(inv.body["invite_url"].(string)))

	def := owner.must(200, owner.do("GET", base+"/layout", nil)).body["layout"].(map[string]any)["mjml"].(string)
	purple := strings.ReplaceAll(def, "#f97316", "#7c3aed")
	pending := owner.must(202, owner.do("PUT", base+"/layout", map[string]any{"mjml": purple, "version": 0}))
	id := pending.body["approval"].(map[string]any)["id"].(string)
	if l := owner.must(200, owner.do("GET", base+"/layout", nil)).body["layout"].(map[string]any); l["is_default"] != true {
		t.Fatalf("the layout changed before approval: %v", l["version"])
	}
	if r := owner.do("POST", "/admin/v1/approvals/"+id+"/approve", map[string]any{}); r.status < 400 {
		t.Fatalf("the requester approved their own layout change: %d", r.status)
	}
	approver.must(200, approver.do("POST", "/admin/v1/approvals/"+id+"/approve", map[string]any{"note": "looks right"}))
	l := owner.must(200, owner.do("GET", base+"/layout", nil)).body["layout"].(map[string]any)
	if num(l["version"]) != 1 || !strings.Contains(l["mjml"].(string), "#7c3aed") {
		t.Fatalf("approved change not applied: %v", l["version"])
	}
	// Templates are rebuilt against it (by the job, or the sweep).
	if err := a.Templates.RebuildStale(ctx); err != nil {
		t.Fatal(err)
	}
	got := owner.must(200, owner.do("GET", base+"/templates/"+tpl["id"].(string), nil)).body["template"].(map[string]any)
	if num(got["layout_version"]) != 1 || got["stale"] != false {
		t.Fatalf("template not rebuilt after the approved change: %v %v", got["layout_version"], got["stale"])
	}
}

// nopRebuilder drops rebuild requests, as if the job were lost.
type nopRebuilder struct{}

func (nopRebuilder) QueueRebuild(context.Context, uuid.UUID) error { return nil }

func issueCodes(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, is := range list {
		out = append(out, is.(map[string]any)["code"].(string))
	}
	return out
}

func num(v any) int {
	f, _ := v.(float64)
	return int(f)
}

func TestTemplates(t *testing.T) {
	a, srv, _ := setup(t)
	ctx := context.Background()

	owner := newBrowser(t, srv.URL)
	link, _ := a.Bootstrap(ctx, "owner@acme.test", "Olivia Owner")
	owner.must(200, owner.acceptInvite(link))
	brandID := owner.must(201, owner.do("POST", "/admin/v1/brands", brightside)).body["brand"].(map[string]any)["id"].(string)
	const base = "/admin/v1/brands/brightside"

	// Until someone saves one, the brand uses the default layout, which passes every check.
	layout := owner.must(200, owner.do("GET", base+"/layout", nil))
	if l := layout.body["layout"].(map[string]any); l["is_default"] != true || num(l["version"]) != 0 {
		t.Fatalf("default layout: %s", layout.raw)
	}
	if codes := issueCodes(layout.body["issues"]); len(codes) != 0 {
		t.Fatalf("default layout issues: %v", codes)
	}
	preview := layout.body["preview_html"].(string)
	if !strings.Contains(preview, "Brightside") || !strings.Contains(preview, "221B Market St") {
		t.Fatal("layout preview is missing the brand name or postal address")
	}

	// A template linking off-brand saves as a draft with an error.
	created := owner.must(201, owner.do("POST", base+"/templates", map[string]any{"name": "Autumn launch", "mjml": welcome}))
	tpl := created.body["template"].(map[string]any)
	id := tpl["id"].(string)
	if num(tpl["error_count"]) != 1 || issueCodes(tpl["issues"])[0] != "link_not_allowed" {
		t.Fatalf("expected link_not_allowed: %s", created.raw)
	}
	if !strings.Contains(created.body["preview_html"].(string), "Hi Alex") || !strings.Contains(created.body["preview_text"].(string), "Hi Alex") {
		t.Fatal("preview should be filled with sample data")
	}
	if r := owner.do("POST", base+"/templates", map[string]any{"name": "autumn LAUNCH", "mjml": welcome}); r.errCode() != "template_name_taken" {
		t.Fatalf("duplicate name: %d %s", r.status, r.raw)
	}

	// Allowlisting the shop fixes it: every template is re-checked.
	for _, d := range []string{"co.uk", "github.io"} {
		if r := owner.do("POST", base+"/link-allowlist", map[string]any{"domain": d}); r.errCode() != "public_suffix" {
			t.Fatalf("public suffix %s: %d %s", d, r.status, r.raw)
		}
	}
	if r := owner.do("POST", base+"/link-allowlist", map[string]any{"domain": "bit.ly"}); r.errCode() != "shortener" {
		t.Fatalf("shortener: %d %s", r.status, r.raw)
	}
	allowed := owner.must(201, owner.do("POST", base+"/link-allowlist", map[string]any{"domain": "https://Shop.Example.com."}))
	if num(allowed.body["templates_queued"]) != 1 {
		t.Fatalf("allowlist rebuild: %s", allowed.raw)
	}
	if d := allowed.body["domains"].([]any)[0].(map[string]any)["domain"]; d != "shop.example.com" {
		t.Fatalf("domain stored as %v", d)
	}
	tpl = owner.must(200, owner.do("GET", base+"/templates/"+id, nil)).body["template"].(map[string]any)
	if num(tpl["error_count"]) != 0 {
		t.Fatalf("still has errors after allowlisting: %v", tpl["issues"])
	}

	// Edits need the version they started from.
	edit := map[string]any{"name": "Autumn launch", "mjml": strings.Replace(welcome, "Shop the range", "See what's new", 1), "version": 1}
	saved := owner.must(200, owner.do("PUT", base+"/templates/"+id, edit))
	if num(saved.body["template"].(map[string]any)["version"]) != 2 {
		t.Fatalf("version not bumped: %s", saved.raw)
	}
	if r := owner.do("PUT", base+"/templates/"+id, edit); r.errCode() != "stale" {
		t.Fatalf("stale edit: %d %s", r.status, r.raw)
	}

	// Preview without saving: MJML errors point at the template's own lines.
	pv := owner.must(200, owner.do("POST", base+"/templates/preview", map[string]any{
		"mjml": "<mj-section><mj-column>\n<mj-text bogus=\"1\">x</mj-text>\n</mj-column></mj-section>",
	}))
	is := pv.body["issues"].([]any)[0].(map[string]any)
	if is["code"] != "mjml" || is["where"] != "template" || num(is["line"]) != 2 {
		t.Fatalf("preview issue: %s", pv.raw)
	}

	// The layout can't lose its unsubscribe link or address.
	def := layout.body["layout"].(map[string]any)["mjml"].(string)
	broken := strings.Replace(strings.Replace(def, `href="{{unsubscribe_url}}"`, `href="https://brightside.co"`, 1), "{{postal_address}}", "", 1)
	r := owner.do("PUT", base+"/layout", map[string]any{"mjml": broken, "version": 0})
	if r.status != 422 || r.errCode() != "layout_has_errors" {
		t.Fatalf("broken layout: %d %s", r.status, r.raw)
	}
	if codes := strings.Join(issueCodes(r.body["issues"]), ","); !strings.Contains(codes, "missing_unsubscribe") || !strings.Contains(codes, "missing_postal_address") {
		t.Fatalf("broken layout issues: %s", codes)
	}

	// A good layout change recompiles every template.
	purple := strings.ReplaceAll(def, "#f97316", "#7c3aed")
	put := owner.must(200, owner.do("PUT", base+"/layout", map[string]any{"mjml": purple, "version": 0}))
	if num(put.body["layout"].(map[string]any)["version"]) != 1 || num(put.body["templates_queued"]) != 1 {
		t.Fatalf("layout save: %s", put.raw)
	}
	tpl = owner.must(200, owner.do("GET", base+"/templates/"+id, nil)).body["template"].(map[string]any)
	if num(tpl["layout_version"]) != 1 || !strings.Contains(tpl["html"].(string), "#7c3aed") || num(tpl["version"]) != 2 {
		t.Fatalf("template not rebuilt with the new layout (or its edit version changed): %v %v", tpl["layout_version"], tpl["version"])
	}
	if r := owner.do("PUT", base+"/layout", map[string]any{"mjml": purple, "version": 0}); r.errCode() != "stale" {
		t.Fatalf("stale layout: %d %s", r.status, r.raw)
	}

	// Layout and allowlist changes need a fresh passkey confirmation.
	if _, err := a.Store.MarkSteppedUpForTest(ctx, "owner@acme.test"); err != nil {
		t.Fatal(err)
	}
	if r := owner.do("PUT", base+"/layout", map[string]any{"mjml": def, "version": 1}); r.errCode() != "step_up_required" {
		t.Fatalf("layout without step-up: %d %s", r.status, r.raw)
	}
	if r := owner.do("DELETE", base+"/link-allowlist/shop.example.com", nil); r.errCode() != "step_up_required" {
		t.Fatalf("allowlist removal without step-up: %d %s", r.status, r.raw)
	}
	if r := owner.do("POST", base+"/link-allowlist", map[string]any{"domain": "blog.example.org"}); r.errCode() != "step_up_required" {
		t.Fatalf("allowlist without step-up: %d %s", r.status, r.raw)
	}
	owner.must(200, owner.stepUp())

	// Roles: editors write templates but not the layout or allowlist; viewers only read.
	invite := func(email, role string) *browser {
		inv := owner.must(201, owner.do("POST", "/admin/v1/team/invites", map[string]any{
			"email": email, "name": email, "company_role": "member", "brand_roles": map[string]string{brandID: role},
		}))
		b := newBrowser(t, srv.URL)
		b.must(200, b.acceptInvite(inv.body["invite_url"].(string)))
		return b
	}
	editor := invite("ed@acme.test", "editor")
	viewer := invite("vi@acme.test", "viewer")

	editorTpl := editor.must(201, editor.do("POST", base+"/templates", map[string]any{"name": "Editor's draft", "mjml": welcome}))
	editorID := editorTpl.body["template"].(map[string]any)["id"].(string)
	editor.must(403, editor.do("PUT", base+"/layout", map[string]any{"mjml": def, "version": 1}))
	editor.must(403, editor.do("POST", base+"/layout/preview", map[string]any{"mjml": def}))
	editor.must(403, editor.do("POST", base+"/link-allowlist", map[string]any{"domain": "blog.example.org"}))
	editor.must(403, editor.do("DELETE", base+"/link-allowlist/shop.example.com", nil))

	list := viewer.must(200, viewer.do("GET", base+"/templates", nil)).body["templates"].([]any)
	if len(list) != 2 {
		t.Fatalf("viewer sees %d templates", len(list))
	}
	viewer.must(200, viewer.do("GET", base+"/layout", nil))
	viewer.must(200, viewer.do("GET", base+"/link-allowlist", nil))
	viewer.must(403, viewer.do("POST", base+"/templates", map[string]any{"name": "x", "mjml": welcome}))
	viewer.must(403, viewer.do("POST", base+"/templates/preview", map[string]any{"mjml": welcome}))
	viewer.must(403, viewer.do("DELETE", base+"/templates/"+editorID, nil))

	// Another brand's templates are invisible, not forbidden.
	owner.must(201, owner.do("POST", "/admin/v1/brands", map[string]any{
		"name": "Nimbus", "slug": "nimbus", "color": "#3b82f6", "postal_address": "1 Cloud Way",
		"sending_domain": "updates.nimbusnotes.app", "link_domain": "go.nimbusnotes.app",
	}))
	editor.must(404, editor.do("GET", "/admin/v1/brands/nimbus/templates", nil))
	editor.must(404, editor.do("GET", "/admin/v1/brands/nimbus/templates/"+id, nil))
	owner.must(404, owner.do("GET", "/admin/v1/brands/nimbus/templates/"+id, nil))

	// Removing an allowlisted domain re-checks templates again.
	removed := owner.must(200, owner.do("DELETE", base+"/link-allowlist/shop.example.com", nil))
	if num(removed.body["templates_queued"]) != 2 {
		t.Fatalf("after removing the domain: %s", removed.raw)
	}
	for _, row := range owner.must(200, owner.do("GET", base+"/templates", nil)).body["templates"].([]any) {
		if r := row.(map[string]any); num(r["error_count"]) != 1 || r["stale"] != false {
			t.Fatalf("templates should be rebuilt with the link error: %v", r)
		}
	}
	owner.must(404, owner.do("DELETE", base+"/link-allowlist/shop.example.com", nil))

	// Archiving hides the template and frees its name.
	editor.must(204, editor.do("DELETE", base+"/templates/"+editorID, nil))
	editor.must(404, editor.do("GET", base+"/templates/"+editorID, nil))
	editor.must(201, editor.do("POST", base+"/templates", map[string]any{"name": "Editor's draft", "mjml": welcome}))

	// If the rebuild job is lost, templates show as stale until the periodic
	// sweep catches up.
	a.Templates.SetRebuilder(nopRebuilder{})
	owner.must(200, owner.do("PUT", base+"/layout", map[string]any{"mjml": def, "version": 1}))
	staleCount := func() int {
		n := 0
		for _, row := range owner.must(200, owner.do("GET", base+"/templates", nil)).body["templates"].([]any) {
			if row.(map[string]any)["stale"] == true {
				n++
			}
		}
		return n
	}
	if n := staleCount(); n != 2 {
		t.Fatalf("want 2 stale templates after a lost rebuild, got %d", n)
	}
	if err := a.Templates.RebuildStale(ctx); err != nil {
		t.Fatal(err)
	}
	if n := staleCount(); n != 0 {
		t.Fatalf("sweep left %d stale templates", n)
	}

	// Everything above is in the audit log.
	entries := owner.must(200, owner.do("GET", "/admin/v1/audit", nil)).body["entries"].([]any)
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.(map[string]any)["action"].(string)] = true
	}
	for _, want := range []string{"template.created", "template.updated", "template.archived", "layout.updated", "link_allowlist.added", "link_allowlist.removed"} {
		if !seen[want] {
			t.Errorf("audit log missing %s", want)
		}
	}
}

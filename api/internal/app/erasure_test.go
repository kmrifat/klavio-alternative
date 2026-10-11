package app_test

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// TestErasure plants a person's data across two brands and every place the
// app keeps it, erases them, and checks what's gone, what's blanked and what
// stays, and that look-alike addresses are left alone.
func TestErasure(t *testing.T) {
	a, srv, dns := setup(t)
	ctx := context.Background()
	owner := newBrowser(t, srv.URL)
	link, _ := a.Bootstrap(ctx, "owner@acme.test", "Olivia Owner")
	owner.must(200, owner.acceptInvite(link))
	dns.mx["example.com"] = []*net.MX{{Host: "mx.example.com.", Pref: 10}}

	northwind := map[string]any{
		"name": "Northwind", "slug": "northwind", "color": "#2563EB",
		"postal_address": "1 Harbour Rd, Seattle", "sending_domain": "news.northwind.co", "link_domain": "go.northwind.co",
	}
	brandIDs := map[string]uuid.UUID{}
	contactIDs := map[string]uuid.UUID{} // "slug/email"
	topics := map[string]any{}
	for _, b := range []map[string]any{brightside, northwind} {
		created := owner.must(201, owner.do("POST", "/admin/v1/brands", b))
		slug := b["slug"].(string)
		brandIDs[slug] = uuid.MustParse(created.body["brand"].(map[string]any)["id"].(string))
		topic := owner.must(200, owner.do("GET", "/admin/v1/brands/"+slug+"/topics", nil)).body["topics"].([]any)[0].(map[string]any)["id"]
		topics[slug] = topic
		for _, e := range []string{"Jane@Example.com", "xjane@example.com", "keep@example.com"} {
			c := owner.must(201, owner.do("POST", "/admin/v1/brands/"+slug+"/contacts", map[string]any{
				"email": e, "topic_ids": []any{topic}, "consent": map[string]string{"basis": "single_opt_in", "source": "Signed up at the shop"},
			}))
			contactIDs[slug+"/"+strings.ToLower(e)] = uuid.MustParse(c.body["id"].(string))
		}
	}
	jane, keep := contactIDs["brightside/jane@example.com"], contactIDs["brightside/keep@example.com"]
	exec := func(sql string, args ...any) {
		t.Helper()
		if err := a.Store.ExecForTest(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := a.Store.QueryRowForTest(ctx, sql, &n, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}

	// Jane got a campaign, clicked and opened it; so did keep@.
	sends := map[uuid.UUID]int64{}
	for _, c := range []uuid.UUID{jane, keep} {
		id, err := a.Store.CreateSendForTest(ctx, brandIDs["brightside"], c)
		if err != nil {
			t.Fatal(err)
		}
		sends[c] = id
		msg := "msg-" + c.String()
		exec(`UPDATE sends SET ses_message_id = $2, error = 'mailbox of ' || (SELECT email FROM contacts WHERE id = $3) || ' was full once' WHERE id = $1`, id, msg, c)
		exec(`INSERT INTO clicks (send_id, brand_id, contact_id, link_no, url, ip, user_agent) VALUES ($1, $2, $3, 0, 'https://brightside.co/', '203.0.113.7', 'Firefox')`, id, brandIDs["brightside"], c)
		exec(`INSERT INTO opens (send_id, brand_id, contact_id, ip, user_agent) VALUES ($1, $2, $3, '203.0.113.7', 'Firefox')`, id, brandIDs["brightside"], c)
		exec(`INSERT INTO email_events (event_id, type, brand_id, ses_message_id, send_id, email_hash, occurred_at, detail)
			SELECT 'ev-' || $1, 'delivery', $2, $1, $3, email_hash, now(), jsonb_build_object('mail', jsonb_build_object('destination', jsonb_build_array(email)))
			FROM contacts WHERE id = $4`, msg, brandIDs["brightside"], id, c)
	}
	// An audit entry from before entries stopped storing addresses.
	exec(`INSERT INTO audit_log (action, target_type, target_id, after) VALUES
		('contact.unsubscribed', 'contact', 'jane@example.com', '{"note": "Jane@example.com called; not xjane@example.com"}')`)
	// A Contacts API reply kept for idempotency, one for each.
	exec(`INSERT INTO api_keys (id, brand_id, name, prefix, key_hash, scopes, expires_at) VALUES ($1, $2, 'site', 'pk_test', '\x01', '{contacts:write}', now() + interval '30 days')`,
		uuid.New(), brandIDs["brightside"])
	for i, e := range []string{"jane@example.com", "keep@example.com"} {
		exec(`INSERT INTO api_idempotency (key_id, idem_key, request_hash, status, response)
			SELECT id, $1, '\x02', 200, jsonb_build_object('email', $2::text) FROM api_keys WHERE prefix = 'pk_test'`, string(rune('a'+i)), e)
	}
	// An import whose preview and samples include Jane.
	exec(`INSERT INTO imports (brand_id, created_by, filename, preview, check_result) SELECT $1, id, 'list.csv',
		'[["JANE@example.com", "Jane"], ["keep@example.com", "Keep"]]',
		'{"samples": {"importable": ["jane@example.com", "keep@example.com"]}}' FROM users LIMIT 1`, brandIDs["northwind"])
	// Two finished export files: one lists Jane (as the brand stored her), one doesn't.
	for i, csv := range []string{"email\nJane@Example.com\nkeep@example.com\n", "email\nkeep@example.com\n"} {
		exec(`WITH ap AS (INSERT INTO approvals (kind, brand_id, requested_by, summary, payload, expires_at)
				SELECT 'export', $1, id, 'Export', '{}', now() + interval '1 day' FROM users LIMIT 1 RETURNING id, requested_by)
			INSERT INTO exports (brand_id, requested_by, approval_id, filter, row_count, file, ready_at, expires_at)
			SELECT $1, requested_by, id, jsonb_build_object('n', $3::int), 2, convert_to($2, 'UTF8'), now(), now() + interval '1 day' FROM ap`,
			brandIDs["brightside"], csv, i)
	}
	consentBefore := count(`SELECT count(*) FROM consent_records WHERE email_hash = $1`, store.EmailHash("jane@example.com"))

	// Members can't use it.
	inv := owner.must(201, owner.do("POST", "/admin/v1/team/invites", map[string]any{
		"email": "mel@acme.test", "name": "Mel Member", "company_role": "member",
		"brand_roles": map[string]any{brandIDs["brightside"].String(): "brand_admin"},
	}))
	mel := newBrowser(t, srv.URL)
	mel.must(200, mel.acceptInvite(inv.body["invite_url"].(string)))
	mel.must(403, mel.do("POST", "/admin/v1/erasures/lookup", map[string]any{"email": "jane@example.com"}))
	mel.must(403, mel.do("GET", "/admin/v1/erasures", nil))

	// Lookup: both brands, the activity, the consent proof and the old audit entry.
	look := owner.must(200, owner.do("POST", "/admin/v1/erasures/lookup", map[string]any{"email": " JANE@example.com "}))
	if n := len(look.body["contacts"].([]any)); n != 2 || look.body["clicks"] != 1.0 || look.body["opens"] != 1.0 ||
		look.body["events"] != 1.0 || look.body["consent_records"] != float64(consentBefore) || look.body["audit_entries"] != 1.0 ||
		look.body["suppression"] != nil || !strings.HasPrefix(look.body["contact_ref"].(string), "j•••@example.com #") {
		t.Fatalf("lookup: %s", look.raw)
	}

	// Erasing needs a fresh passkey, the address typed twice, and a reference without it.
	if _, err := a.Store.MarkSteppedUpForTest(ctx, "owner@acme.test"); err != nil {
		t.Fatal(err)
	}
	req := map[string]any{"email": "jane@example.com", "confirm_email": "Jane@Example.com", "reference": "Email to privacy@, 3 Oct"}
	if r := owner.do("POST", "/admin/v1/erasures", req); r.errCode() != "step_up_required" {
		t.Fatalf("want step_up_required: %s", r.raw)
	}
	owner.must(200, owner.stepUp())
	for code, edit := range map[string]func(map[string]any){
		"confirmation_mismatch":  func(m map[string]any) { m["confirm_email"] = "jane@example.org" },
		"reference_required":     func(m map[string]any) { m["reference"] = " " },
		"reference_names_person": func(m map[string]any) { m["reference"] = "Request from JANE@example.com" },
		"invalid_email":          func(m map[string]any) { m["email"] = "jane" },
	} {
		bad := map[string]any{}
		for k, v := range req {
			bad[k] = v
		}
		edit(bad)
		if r := owner.do("POST", "/admin/v1/erasures", bad); r.status != 400 || r.errCode() != code {
			t.Errorf("%s: %d %s", code, r.status, r.raw)
		}
	}
	er := owner.must(201, owner.do("POST", "/admin/v1/erasures", req)).body["erasure"].(map[string]any)
	sum := er["summary"].(map[string]any)
	if sum["contacts_deleted"] != 2.0 || sum["clicks_blanked"] != 1.0 || sum["opens_blanked"] != 1.0 || sum["events_blanked"] != 1.0 ||
		sum["send_errors_blanked"] != 1.0 || sum["api_replies_deleted"] != 1.0 || sum["imports_scrubbed"] != 1.0 ||
		sum["audit_entries_redacted"] != 1.0 || sum["exports_deleted"] != 1.0 || sum["consent_records_kept"] != float64(consentBefore) || er["reference"] != "Email to privacy@, 3 Oct" {
		t.Fatalf("summary: %v", er)
	}

	// Gone: the contacts, everywhere. Look-alikes and bystanders stay.
	hash := store.EmailHash("jane@example.com")
	if n := count(`SELECT count(*) FROM contacts WHERE email_hash = $1`, hash); n != 0 {
		t.Fatalf("%d contacts left", n)
	}
	if n := count(`SELECT count(*) FROM contacts WHERE email IN ('xjane@example.com', 'keep@example.com')`); n != 4 {
		t.Fatalf("bystanders: %d contacts", n)
	}
	if n := count(`SELECT count(*) FROM suppressions WHERE email_hash = $1 AND reason = 'erasure' AND note = ''`, hash); n != 1 {
		t.Fatal("address not blocked")
	}
	// Blanked for Jane only; the rows stay so reports keep their totals.
	for _, q := range []string{
		`SELECT count(*) FROM clicks WHERE contact_id = $1 AND ip = '' AND user_agent = ''`,
		`SELECT count(*) FROM opens WHERE contact_id = $1 AND ip = '' AND user_agent = ''`,
		`SELECT count(*) FROM sends WHERE contact_id = $1 AND error = ''`,
		`SELECT count(*) FROM email_events WHERE send_id = (SELECT id FROM sends WHERE contact_id = $1) AND detail = '{}'`,
	} {
		if count(q, jane) != 1 || count(q, keep) != 0 {
			t.Errorf("blanked for the wrong person: %s", q)
		}
	}
	if n := count(`SELECT count(*) FROM api_idempotency WHERE response->>'email' = 'keep@example.com'`); n != 1 {
		t.Error("keep@'s API reply was deleted")
	}
	if n := count(`SELECT count(*) FROM exports WHERE file IS NULL AND filter->>'n' = '0'`) + count(`SELECT count(*) FROM exports WHERE file IS NOT NULL AND filter->>'n' = '1'`); n != 2 {
		t.Error("the wrong export file was deleted")
	}
	var preview, samples string
	if err := a.Store.QueryRowForTest(ctx, `SELECT preview::text FROM imports`, &preview); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.QueryRowForTest(ctx, `SELECT (check_result->'samples')::text FROM imports`, &samples); err != nil {
		t.Fatal(err)
	}
	if preview != `[["keep@example.com", "Keep"]]` || samples != `{"importable": ["keep@example.com"]}` {
		t.Fatalf("import: %s %s", preview, samples)
	}
	// The audit log names her only by reference now; xjane@ is untouched.
	ref := store.ContactRef("jane@example.com")
	var target, after string
	if err := a.Store.QueryRowForTest(ctx, `SELECT target_id || '|' || after::text FROM audit_log WHERE action = 'contact.unsubscribed'`, &target); err != nil {
		t.Fatal(err)
	}
	target, after, _ = strings.Cut(target, "|")
	if target != ref || !strings.Contains(after, ref+" called; not xjane@example.com") {
		t.Fatalf("audit: %q %q", target, after)
	}
	if n := count(`SELECT count(*) FROM audit_log WHERE (target_id || coalesce(after::text, '')) ~* '(^|[^x])jane@example\.com'`); n != 0 {
		t.Fatalf("%d audit entries still name her", n)
	}
	if n := count(`SELECT count(*) FROM audit_log WHERE action = 'contact.erased' AND target_id = $1`, er["id"]); n != 1 {
		t.Fatal("erasure not audited")
	}
	// The audit log is otherwise as locked as before.
	if _, err := a.Store.TryEditAuditForTest(ctx); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("audit log editable: %v", err)
	}
	// Kept: consent proof, under the hash.
	if n := count(`SELECT count(*) FROM consent_records WHERE email_hash = $1`, hash); n != consentBefore {
		t.Fatalf("consent records: %d, want %d", n, consentBefore)
	}

	// She can't be added back, and a lookup shows the erasure.
	if r := owner.do("POST", "/admin/v1/brands/brightside/contacts", map[string]any{
		"email": "jane@example.com", "topic_ids": []any{topics["brightside"]},
		"consent": map[string]string{"basis": "single_opt_in", "source": "again"},
	}); r.errCode() != "suppressed" {
		t.Fatalf("re-add: %s", r.raw)
	}
	look = owner.must(200, owner.do("POST", "/admin/v1/erasures/lookup", map[string]any{"email": "jane@example.com"}))
	if look.body["suppression"] != "erasure" || len(look.body["contacts"].([]any)) != 0 || len(look.body["erasures"].([]any)) != 1 {
		t.Fatalf("lookup after: %s", look.raw)
	}
	list := owner.must(200, owner.do("GET", "/admin/v1/erasures", nil)).body["erasures"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["erased_by_name"] != "Olivia Owner" || list[0].(map[string]any)["contact_ref"] != ref {
		t.Fatalf("list: %v", list)
	}

	// Erasing an address nobody has still blocks it.
	owner.must(201, owner.do("POST", "/admin/v1/erasures", map[string]any{
		"email": "nobody@example.com", "confirm_email": "nobody@example.com", "reference": "Letter, 4 Oct"}))
	if n := count(`SELECT count(*) FROM suppressions WHERE email_hash = $1 AND reason = 'erasure'`, store.EmailHash("nobody@example.com")); n != 1 {
		t.Fatal("unknown address not blocked")
	}
}

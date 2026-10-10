package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

func (b *browser) upload(path, filename, content string) resp {
	b.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", filename)
	_, _ = io.WriteString(fw, content)
	_ = mw.Close()
	req, _ := http.NewRequest("POST", b.base+path, &body)
	req.Header.Set("Origin", origin)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := b.http.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, raw: raw}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func get[T any](t *testing.T, m map[string]any, path ...string) T {
	t.Helper()
	var cur any = m
	for _, p := range path {
		cur = cur.(map[string]any)[p]
	}
	v, ok := cur.(T)
	if !ok {
		t.Fatalf("%v: got %T (%v)", path, cur, cur)
	}
	return v
}

func TestImportFlow(t *testing.T) {
	a, srv, dns := setup(t)
	ctx := context.Background()
	owner := newBrowser(t, srv.URL)
	link, _ := a.Bootstrap(ctx, "owner@acme.test", "Olivia Owner")
	owner.must(200, owner.acceptInvite(link))
	b := owner.must(201, owner.do("POST", "/admin/v1/brands", brightside)).body["brand"].(map[string]any)
	base := "/admin/v1/brands/brightside"

	topics := owner.must(200, owner.do("GET", base+"/topics", nil)).body["topics"].([]any)
	if len(topics) != 1 || topics[0].(map[string]any)["name"] != "Newsletter" {
		t.Fatalf("default topic: %v", topics)
	}
	newsletter := topics[0].(map[string]any)["id"].(string)

	// Domains that take mail; deadmail.test doesn't exist.
	for _, d := range []string{"example.com", "acme.org"} {
		dns.mx[d] = []*net.MX{{Host: "mx." + d + ".", Pref: 10}}
	}

	// Someone already blocked company-wide, and someone who unsubscribed earlier.
	if err := a.Store.Suppress(ctx, "blocked@example.com", store.SuppressComplaint, nil, "", nil); err != nil {
		t.Fatal(err)
	}
	gone := owner.must(201, owner.do("POST", base+"/contacts", map[string]any{
		"email": "gone@example.com", "topic_ids": []string{newsletter},
		"consent": map[string]string{"basis": "single_opt_in", "source": "Signed up at the shop"},
	}))
	owner.must(204, owner.do("POST", base+"/contacts/"+gone.body["id"].(string)+"/unsubscribe", map[string]any{"note": "asked by phone"}))

	recent := time.Now().AddDate(0, 0, -10).Format("2006-01-02 15:04:05")
	warm := time.Now().AddDate(0, 0, -60).Format("2006-01-02 15:04:05")
	old := time.Now().AddDate(-3, 0, 0).Format("2006-01-02 15:04:05")
	csv := strings.Join([]string{
		`"Email Address","First Name","Last Name","CONFIRM_TIME","TAGS"`,
		fmt.Sprintf(`maya@example.com,Maya,R,"%s","""vip"""`, recent),
		fmt.Sprintf(`daniel@acme.org,Daniel,K,"%s",`, warm),
		fmt.Sprintf(`stale@example.com,Stale,S,"%s",`, old),
		fmt.Sprintf(`info@example.com,,,"%s",`, recent),
		fmt.Sprintf(`temp@mailinator.com,,,"%s",`, recent),
		fmt.Sprintf(`someone@deadmail.test,,,"%s",`, recent),
		`not-an-email,,,,`,
		fmt.Sprintf(`MAYA@example.com,Maya,R,"%s",`, recent),
		fmt.Sprintf(`blocked@example.com,,,"%s",`, recent),
		fmt.Sprintf(`gone@example.com,,,"%s",`, recent),
	}, "\n")
	// Pad with good rows so the invalid/role rates stay under the gate limits.
	for i := range 100 {
		csv += fmt.Sprintf("\nperson%d@example.com,P,%d,\"%s\",", i, i, recent)
	}

	up := owner.upload(base+"/imports", "mailchimp.csv", csv)
	owner.must(201, up)
	imp := up.body["import"].(map[string]any)
	id := imp["id"].(string)
	if imp["row_count"].(float64) != 110 || get[string](t, imp, "mapping", "consented_at") != "CONFIRM_TIME" {
		t.Fatalf("upload: %s", up.raw)
	}

	setupBody := func(attested bool) map[string]any {
		return map[string]any{
			"mapping": imp["mapping"], "topic_ids": []string{newsletter},
			"consent": map[string]any{
				"source": "platform_migration", "source_detail": "Mailchimp export", "collected_period": "2023–2026",
				"wording": "brightside.co/subscribe", "legal_basis": "consent", "attested": attested,
			},
		}
	}

	// Not attested: the check runs but the gate fails, and commit is refused.
	owner.must(200, owner.do("PUT", base+"/imports/"+id, setupBody(false)))
	checked := owner.must(202, owner.do("POST", base+"/imports/"+id+"/check", nil))
	if get[bool](t, checked.body, "import", "check_result", "gate", "passed") {
		t.Fatalf("unattested import passed the gate: %s", checked.raw)
	}
	if r := owner.do("POST", base+"/imports/"+id+"/commit", nil); r.errCode() != "import_gate_failed" {
		t.Fatalf("commit without attestation: %d %s", r.status, r.raw)
	}

	// Attested: every outcome is counted, gate passes.
	owner.must(200, owner.do("PUT", base+"/imports/"+id, setupBody(true)))
	checked = owner.must(202, owner.do("POST", base+"/imports/"+id+"/check", nil))
	res := get[map[string]any](t, checked.body, "import", "check_result")
	counts := res["counts"].(map[string]any)
	want := map[string]float64{
		"ready": 102, "quarantine": 1, "role": 1, "disposable": 1, "no_mailbox": 1,
		"invalid": 1, "duplicate": 1, "suppressed": 1, "unsubscribed": 1,
	}
	for k, v := range want {
		if counts[k] != v {
			t.Errorf("count %s = %v, want %v (all: %v)", k, counts[k], v, counts)
		}
	}
	if !get[bool](t, res, "gate", "passed") {
		t.Fatalf("gate should pass: %v", res["gate"])
	}

	done := owner.must(202, owner.do("POST", base+"/imports/"+id+"/commit", nil))
	if get[string](t, done.body, "import", "status") != "done" {
		t.Fatalf("import not done: %s", done.raw)
	}
	result := get[map[string]any](t, done.body, "import", "import_result")
	if result["created"] != 102.0 || result["quarantined"] != 1.0 || result["left_unsubscribed"] != 1.0 {
		t.Fatalf("result: %v", result)
	}
	if r := owner.do("POST", base+"/imports/"+id+"/commit", nil); r.status != 409 {
		t.Fatalf("second commit should be refused: %d", r.status)
	}

	// S3 numbers: 101 active (10 days), 1 warm (60 days), 1 quarantined, 1 unsubscribed.
	stats := owner.must(200, owner.do("GET", base+"/contacts/stats", nil)).body
	tiers := stats["tiers"].(map[string]any)
	if tiers["active"] != 101.0 || tiers["warm"] != 1.0 || stats["quarantined"] != 1.0 || stats["unsubscribed"] != 1.0 {
		t.Fatalf("stats: %v", stats)
	}

	// The unsubscribe survived the import.
	goneNow := owner.must(200, owner.do("GET", base+"/contacts/"+gone.body["id"].(string), nil)).body
	if get[string](t, goneNow, "contact", "status") != "unsubscribed" {
		t.Fatalf("import re-subscribed someone: %v", goneNow["contact"])
	}

	// Filters and consent proof.
	warmList := owner.must(200, owner.do("GET", base+"/contacts?tier=warm", nil)).body["contacts"].([]any)
	if len(warmList) != 1 || warmList[0].(map[string]any)["email"] != "daniel@acme.org" {
		t.Fatalf("warm list: %v", warmList)
	}
	vip := owner.must(200, owner.do("GET", base+"/contacts?tag=vip", nil)).body["contacts"].([]any)
	if len(vip) != 1 {
		t.Fatalf("vip list: %v", vip)
	}
	maya := vip[0].(map[string]any)
	mayaDetail := owner.must(200, owner.do("GET", base+"/contacts/"+maya["id"].(string), nil)).body
	history := mayaDetail["consent_history"].([]any)
	if len(history) != 1 || history[0].(map[string]any)["kind"] != "import_attested" ||
		!strings.Contains(history[0].(map[string]any)["source"].(string), "Mailchimp export") {
		t.Fatalf("consent history: %v", history)
	}

	// Paging.
	page := owner.must(200, owner.do("GET", base+"/contacts?limit=50", nil)).body
	if len(page["contacts"].([]any)) != 50 || page["next_after"] == "" {
		t.Fatalf("first page: %d next=%v", len(page["contacts"].([]any)), page["next_after"])
	}
	// 102 imported + 1 quarantined + 1 unsubscribed = 104 → pages of 50, 50, 4.
	seenEmails := map[string]bool{}
	after, pages := "", 0
	for {
		pg := owner.must(200, owner.do("GET", base+"/contacts?limit=50&after="+after, nil)).body
		for _, c := range pg["contacts"].([]any) {
			seenEmails[c.(map[string]any)["email"].(string)] = true
		}
		pages++
		if after = pg["next_after"].(string); after == "" {
			break
		}
	}
	if len(seenEmails) != 104 || pages != 3 {
		t.Fatalf("paging saw %d contacts in %d pages", len(seenEmails), pages)
	}

	// Suppressing blocks the address for every brand.
	owner.must(204, owner.do("POST", base+"/contacts/"+maya["id"].(string)+"/suppress", map[string]any{"note": "asked never to be emailed"}))
	if r := owner.do("POST", base+"/contacts", map[string]any{
		"email": "maya@example.com", "topic_ids": []string{newsletter},
		"consent": map[string]string{"basis": "single_opt_in", "source": "x"},
	}); r.errCode() != "suppressed" {
		t.Fatalf("re-adding a suppressed address: %d %s", r.status, r.raw)
	}

	// A viewer sees counts but not the people.
	inv := owner.must(201, owner.do("POST", "/admin/v1/team/invites", map[string]any{
		"email": "viewer@acme.test", "name": "Vic Viewer", "brand_roles": map[string]string{b["id"].(string): "viewer"},
	}))
	viewer := newBrowser(t, srv.URL)
	viewer.must(200, viewer.acceptInvite(inv.body["invite_url"].(string)))
	viewer.must(200, viewer.do("GET", base+"/contacts/stats", nil))
	viewer.must(403, viewer.do("GET", base+"/contacts", nil))
	viewer.must(403, viewer.upload(base+"/imports", "x.csv", "email\na@example.com\n"))

	// Audit trail: attestation, start, finish.
	entries := owner.must(200, owner.do("GET", "/admin/v1/audit", nil)).body["entries"].([]any)
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.(map[string]any)["action"].(string)] = true
	}
	for _, w := range []string{"import.attested", "import.started", "import.finished", "contact.suppressed", "contact.unsubscribed"} {
		if !seen[w] {
			t.Errorf("audit missing %s", w)
		}
	}
}

func TestImportRejectsBadFiles(t *testing.T) {
	a, srv, _ := setup(t)
	owner := newBrowser(t, srv.URL)
	link, _ := a.Bootstrap(context.Background(), "owner@acme.test", "Olivia Owner")
	owner.must(200, owner.acceptInvite(link))
	owner.must(201, owner.do("POST", "/admin/v1/brands", brightside))
	base := "/admin/v1/brands/brightside/imports"

	if r := owner.upload(base, "empty.csv", ""); r.errCode() != "bad_file" {
		t.Fatalf("empty: %d %s", r.status, r.raw)
	}
	up := owner.must(201, owner.upload(base, "x.csv", "name,phone\nA,1\n"))
	id := up.body["import"].(map[string]any)["id"].(string)
	if r := owner.do("PUT", base+"/"+id, map[string]any{"mapping": map[string]string{"email": "nope"}, "topic_ids": []string{}}); r.errCode() != "bad_mapping" {
		t.Fatalf("bad mapping: %d %s", r.status, r.raw)
	}
	owner.must(200, owner.do("POST", base+"/"+id+"/cancel", nil))
	if r := owner.do("POST", base+"/"+id+"/check", nil); r.status != 400 && r.status != 409 {
		t.Fatalf("check after cancel: %d %s", r.status, r.raw)
	}
}

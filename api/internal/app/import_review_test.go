package app_test

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// importSetup signs in an owner, creates Brightside and returns helpers.
func importSetup(t *testing.T) (*browser, func(csv string, consent map[string]any, mapping ...map[string]string) map[string]any, string) {
	a, srv, dns := setup(t)
	_ = a
	owner := newBrowser(t, srv.URL)
	link, _ := a.Bootstrap(context.Background(), "owner@acme.test", "Olivia Owner")
	owner.must(200, owner.acceptInvite(link))
	owner.must(201, owner.do("POST", "/admin/v1/brands", brightside))
	base := "/admin/v1/brands/brightside"
	topic := owner.must(200, owner.do("GET", base+"/topics", nil)).body["topics"].([]any)[0].(map[string]any)["id"].(string)
	dns.mx["example.com"] = []*net.MX{{Host: "mx.example.com.", Pref: 10}}

	run := func(csv string, consent map[string]any, mapping ...map[string]string) map[string]any {
		imp := owner.must(201, owner.upload(base+"/imports", "x.csv", csv)).body["import"].(map[string]any)
		id := imp["id"].(string)
		for _, m := range mapping {
			for k, v := range m {
				imp["mapping"].(map[string]any)[k] = v
			}
		}
		c := map[string]any{"source": "website_form", "collected_period": "2026", "wording": "x", "legal_basis": "consent", "attested": true}
		for k, v := range consent {
			c[k] = v
		}
		owner.must(200, owner.do("PUT", base+"/imports/"+id, map[string]any{"mapping": imp["mapping"], "topic_ids": []string{topic}, "consent": c}))
		checked := owner.must(202, owner.do("POST", base+"/imports/"+id+"/check", nil)).body["import"].(map[string]any)
		owner.must(202, owner.do("POST", base+"/imports/"+id+"/commit", nil))
		return checked
	}
	return owner, run, base
}

func tierOf(t *testing.T, b *browser, base, email string) map[string]any {
	t.Helper()
	list := b.must(200, b.do("GET", base+"/contacts?q="+email, nil)).body["contacts"].([]any)
	if len(list) != 1 {
		t.Fatalf("%s: found %d contacts", email, len(list))
	}
	return list[0].(map[string]any)
}

// Review item 2: an EU contact rated 5 stars must not become Active.
func TestImportEURatedFiveIsNotActive(t *testing.T) {
	owner, run, base := importSetup(t)
	consent := time.Now().AddDate(0, 0, -60).Format("2006-01-02")
	csv := "Email Address,MEMBER_RATING,CONFIRM_TIME,CC\n" +
		fmt.Sprintf("eu@example.com,5,%s,DE\n", consent) +
		fmt.Sprintf("us@example.com,5,%s,US\n", consent) +
		fmt.Sprintf("eu-old@example.com,5,%s,FR\n", time.Now().AddDate(-2, 0, 0).Format("2006-01-02"))
	checked := run(csv, nil) // default tracking mode: by country
	if n := checked["check_result"].(map[string]any)["no_tracking"]; n != 2.0 {
		t.Fatalf("no_tracking = %v, want 2", n)
	}
	eu := tierOf(t, owner, base, "eu@example.com")
	if eu["tier"] != "warm" || eu["tracking_allowed"] != false {
		t.Fatalf("EU rated-5 contact: tier %v tracking %v, want warm/false", eu["tier"], eu["tracking_allowed"])
	}
	if us := tierOf(t, owner, base, "us@example.com"); us["tier"] != "active" {
		t.Fatalf("US rated-5 contact: tier %v, want active", us["tier"])
	}
	// Stars don't rescue an EU contact from quarantine either.
	if old := tierOf(t, owner, base, "eu-old@example.com"); old["status"] != "quarantined" {
		t.Fatalf("old EU rated-5 contact: status %v, want quarantined", old["status"])
	}
}

func TestImportTrackingModes(t *testing.T) {
	owner, run, base := importSetup(t)
	consent := time.Now().AddDate(0, 0, -5).Format("2006-01-02")
	run(fmt.Sprintf("email,date,tracking\nyes@example.com,%s,yes\nno@example.com,%s,no\n", consent, consent),
		map[string]any{"tracking": "column"}, map[string]string{"email": "email", "consented_at": "date", "tracking": "tracking"})
	if c := tierOf(t, owner, base, "yes@example.com"); c["tier"] != "active" {
		t.Fatalf("column yes: %v", c["tier"])
	}
	if c := tierOf(t, owner, base, "no@example.com"); c["tier"] != "warm" || c["tracking_allowed"] != false {
		t.Fatalf("column no: %v %v", c["tier"], c["tracking_allowed"])
	}
}

// Review items 3 and 4: future dates and long multi-byte names.
func TestImportFutureDatesAndCJKNames(t *testing.T) {
	owner, run, base := importSetup(t)
	cjk := strings.Repeat("田中倫", 50) // 150 characters
	csv := "Email Address,First Name,CONFIRM_TIME\n" +
		"future@example.com,Fu,2206-03-01 00:00:00\n" +
		fmt.Sprintf("cjk@example.com,%s,%s\n", cjk, time.Now().AddDate(0, 0, -3).Format("2006-01-02"))
	run(csv, nil)
	if c := tierOf(t, owner, base, "future@example.com"); c["status"] != "quarantined" || c["consented_at"] != nil {
		t.Fatalf("future-dated row: status %v consented_at %v", c["status"], c["consented_at"])
	}
	c := tierOf(t, owner, base, "cjk@example.com")
	if n := len([]rune(c["first_name"].(string))); n != 100 {
		t.Fatalf("CJK name kept %d characters, want 100", n)
	}
}

// Review items 1 and 5: jobs can't leave an import stuck, replays are no-ops,
// and the preview is served without the file.
func TestImportJobRobustness(t *testing.T) {
	a, srv, dns := setup(t)
	ctx := context.Background()
	owner := newBrowser(t, srv.URL)
	link, _ := a.Bootstrap(ctx, "owner@acme.test", "Olivia Owner")
	owner.must(200, owner.acceptInvite(link))
	b := owner.must(201, owner.do("POST", "/admin/v1/brands", brightside)).body["brand"].(map[string]any)
	brandID := uuid.MustParse(b["id"].(string))
	base := "/admin/v1/brands/brightside/imports"
	topic := owner.must(200, owner.do("GET", "/admin/v1/brands/brightside/topics", nil)).body["topics"].([]any)[0].(map[string]any)["id"].(string)
	dns.mx["example.com"] = []*net.MX{{Host: "mx.example.com.", Pref: 10}}

	csv := "email,date\nmaya@example.com," + time.Now().AddDate(0, 0, -3).Format("2006-01-02") + "\n"
	up := owner.must(201, owner.upload(base, "x.csv", csv)).body
	imp := up["import"].(map[string]any)
	id := uuid.MustParse(imp["id"].(string))
	if p := up["preview"].([]any); len(p) != 1 {
		t.Fatalf("preview: %v", p)
	}
	owner.must(200, owner.do("PUT", base+"/"+id.String(), map[string]any{"mapping": imp["mapping"], "topic_ids": []string{topic},
		"consent": map[string]any{"source": "website_form", "collected_period": "2026", "wording": "x", "legal_basis": "consent", "attested": true}}))

	// A check whose job times out (DNS hangs) ends as failed, not "checking" forever.
	dns.mu.Lock()
	dns.hang = true
	dns.mu.Unlock()
	if err := a.Store.TransitionImport(ctx, id, []store.ImportStatus{store.ImportUploaded}, store.ImportChecking); err != nil {
		t.Fatal(err)
	}
	jobCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if err := a.Imports.RunCheck(jobCtx, brandID, id); err != nil {
		t.Fatalf("RunCheck: %v", err)
	}
	got := owner.must(200, owner.do("GET", base+"/"+id.String(), nil)).body["import"].(map[string]any)
	if got["status"] != "failed" || !strings.Contains(got["error"].(string), "safety check failed") {
		t.Fatalf("after timeout: %v %v", got["status"], got["error"])
	}
	dns.mu.Lock()
	dns.hang = false
	dns.mu.Unlock()

	// A run stuck in "checking" can be cancelled only once it's stale; cancelling drops the file.
	if err := a.Store.TransitionImport(ctx, id, []store.ImportStatus{store.ImportFailed}, store.ImportChecking); err != nil {
		t.Fatal(err)
	}
	if r := owner.do("POST", base+"/"+id.String()+"/cancel", nil); r.status != 409 {
		t.Fatalf("cancelling a fresh run: %d %s", r.status, r.raw)
	}
	if err := a.Store.BackdateImportForTest(ctx, id, store.StaleRunAfter+time.Minute); err != nil {
		t.Fatal(err)
	}
	owner.must(200, owner.do("POST", base+"/"+id.String()+"/cancel", nil))
	if f, _ := a.Store.ImportFile(ctx, id); f != nil {
		t.Fatal("cancelled import still holds the file")
	}
	// A late job for the cancelled import does nothing.
	if err := a.Imports.RunCheck(ctx, brandID, id); err != nil {
		t.Fatal(err)
	}
	if st, _ := a.Store.ImportStatusOf(ctx, id); st != store.ImportCancelled {
		t.Fatalf("late job changed a cancelled import to %s", st)
	}

	// A finished import: replaying the commit job is a no-op; the preview still shows.
	up2 := owner.must(201, owner.upload(base, "y.csv", csv)).body["import"].(map[string]any)
	id2 := uuid.MustParse(up2["id"].(string))
	owner.must(200, owner.do("PUT", base+"/"+id2.String(), map[string]any{"mapping": up2["mapping"], "topic_ids": []string{topic},
		"consent": map[string]any{"source": "website_form", "collected_period": "2026", "wording": "x", "legal_basis": "consent", "attested": true}}))
	owner.must(202, owner.do("POST", base+"/"+id2.String()+"/check", nil))
	done := owner.must(202, owner.do("POST", base+"/"+id2.String()+"/commit", nil)).body
	if done["import"].(map[string]any)["status"] != "done" {
		t.Fatalf("not done: %v", done["import"])
	}
	if err := a.Imports.RunCommit(ctx, brandID, id2); err != nil {
		t.Fatal(err)
	}
	after := owner.must(200, owner.do("GET", base+"/"+id2.String(), nil)).body
	if after["import"].(map[string]any)["status"] != "done" || len(after["preview"].([]any)) != 1 {
		t.Fatalf("after replay: %v, preview %v", after["import"].(map[string]any)["status"], after["preview"])
	}
	if f, _ := a.Store.ImportFile(ctx, id2); f != nil {
		t.Fatal("finished import still holds the file")
	}
}

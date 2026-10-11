package seed_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/seed"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/testdb"
)

func TestSeed(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.New(pool)
	small := seed.Options{Scale: 0.25} // a quarter of the contacts keeps the test quick
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := st.QueryRowForTest(ctx, sql, &n, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}

	if err := seed.Run(ctx, pool, log, small); err == nil || !strings.Contains(err.Error(), "no owner") {
		t.Fatalf("without an owner: %v", err)
	}
	if err := st.ExecForTest(ctx, `INSERT INTO users (email, name, company_role, webauthn_id) VALUES ('owner@acme.test', 'Olivia', 'owner', '\x01')`); err != nil {
		t.Fatal(err)
	}
	if err := seed.Run(ctx, pool, log, small); err != nil {
		t.Fatal(err)
	}

	if n := count(`SELECT count(*) FROM brands WHERE status = 'ready' AND sending_domain LIKE '%.test'`); n != 3 {
		t.Fatalf("%d ready demo brands", n)
	}
	total := 0
	for _, b := range seed.Brands {
		total += int(float64(b.Contacts) * small.Scale)
	}
	if n := count(`SELECT count(*) FROM contacts`); n != total {
		t.Fatalf("%d contacts, want %d", n, total)
	}
	// Every address is undeliverable by design.
	if n := count(`SELECT count(*) FROM contacts WHERE email NOT LIKE '%.test'`); n != 0 {
		t.Fatalf("%d contacts outside .test", n)
	}
	if n := count(`SELECT count(*) FROM templates WHERE html <> '' AND error_count = 0`); n != 6 {
		t.Fatalf("%d compiled templates", n)
	}
	if n := count(`SELECT count(*) FROM campaigns WHERE state = 'done'`); n != 18 {
		t.Fatalf("%d sent campaigns", n)
	}
	if n := count(`SELECT count(*) FROM campaigns WHERE state = 'draft'`); n != 3 {
		t.Fatalf("%d drafts", n)
	}
	for _, q := range []string{
		`SELECT count(*) FROM sends`, `SELECT count(*) FROM opens`, `SELECT count(*) FROM clicks WHERE NOT is_bot`,
		`SELECT count(*) FROM clicks WHERE is_bot`, `SELECT count(*) FROM suppressions WHERE reason = 'hard_bounce'`,
		`SELECT count(*) FROM consent_records WHERE kind = 'unsubscribed' AND campaign_id IS NOT NULL`,
		`SELECT count(*) FROM marketing_sends`, `SELECT count(*) FROM segments`,
		`SELECT count(*) FROM contacts WHERE last_engaged_at > now() - interval '30 days'`,
	} {
		if count(q) == 0 {
			t.Errorf("nothing for %s", q)
		}
	}
	// Nobody got a campaign after unsubscribing, or while suppressed.
	if n := count(`SELECT count(*) FROM sends s JOIN consent_records r ON r.contact_id = s.contact_id
		WHERE r.kind = 'unsubscribed' AND s.sent_at > r.occurred_at`); n != 0 {
		t.Errorf("%d sends after an unsubscribe", n)
	}
	if n := count(`SELECT count(*) FROM sends s JOIN contacts c ON c.id = s.contact_id JOIN suppressions p ON p.email_hash = c.email_hash
		WHERE s.sent_at > p.created_at`); n != 0 {
		t.Errorf("%d sends to suppressed addresses", n)
	}

	// The reports read sensible numbers: Harbor's latest campaign bounces
	// a little over 1%, the others well under.
	var latest map[string]uuid.UUID = map[string]uuid.UUID{}
	for _, slug := range []string{"harbor", "northwind"} {
		var id uuid.UUID
		if err := st.QueryRowForTest(ctx, `SELECT c.id FROM campaigns c JOIN brands b ON b.id = c.brand_id
			WHERE b.slug = $1 AND c.state = 'done' ORDER BY c.start_at DESC LIMIT 1`, &id, slug); err != nil {
			t.Fatal(err)
		}
		latest[slug] = id
	}
	for slug, lo := range map[string][2]float64{"harbor": {0.9, 2}, "northwind": {0, 0.9}} {
		f, err := st.CampaignFeedback(ctx, latest[slug])
		if err != nil {
			t.Fatal(err)
		}
		if rate := 100 * float64(f.Bounces) / float64(f.Delivered+f.Bounces); rate < lo[0] || rate >= lo[1] || f.Delivered < 250 {
			t.Errorf("%s: %d delivered, bounce rate %.2f%%", slug, f.Delivered, rate)
		}
	}

	// Running it again needs reset; with it, the same data comes back.
	if err := seed.Run(ctx, pool, log, small); !errors.Is(err, seed.ErrAlreadySeeded) {
		t.Fatalf("second run: %v", err)
	}
	var firstEmail string
	if err := st.QueryRowForTest(ctx, `SELECT min(email::text) FROM contacts`, &firstEmail); err != nil {
		t.Fatal(err)
	}
	if err := seed.Run(ctx, pool, log, seed.Options{Reset: true, Scale: small.Scale}); err != nil {
		t.Fatal(err)
	}
	var again string
	if err := st.QueryRowForTest(ctx, `SELECT min(email::text) FROM contacts`, &again); err != nil {
		t.Fatal(err)
	}
	// The contacts come back identical; which of them clicked or bounced
	// can differ, since rows come back in another order after a delete.
	if count(`SELECT count(*) FROM contacts`) != total || again != firstEmail || count(`SELECT count(*) FROM campaigns WHERE state = 'done'`) != 18 {
		t.Fatalf("reset didn't bring back the same data: %d contacts, %q vs %q, %d sent campaigns",
			count(`SELECT count(*) FROM contacts`), firstEmail, again, count(`SELECT count(*) FROM campaigns WHERE state = 'done'`))
	}

	// It never touches a real brand that happens to use a demo slug.
	if err := st.ExecForTest(ctx, `UPDATE brands SET sending_domain = 'news.harbor-books.com' WHERE slug = 'harbor'`); err != nil {
		t.Fatal(err)
	}
	if err := seed.Run(ctx, pool, log, seed.Options{Reset: true, Scale: small.Scale}); err == nil || !strings.Contains(err.Error(), "real brand") {
		t.Fatalf("real brand: %v", err)
	}
}

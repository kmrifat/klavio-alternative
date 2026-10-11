package store_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/testdb"
)

// TestEventRetention: old events of a finished campaign are deleted after
// its totals are saved, and its report doesn't change. Events of campaigns
// still running, recent events and recent orphans stay.
func TestEventRetention(t *testing.T) {
	st := store.New(testdb.New(t))
	ctx := context.Background()
	b := newBrand(t, st, "brightside")
	exec := func(sql string, args ...any) {
		t.Helper()
		if err := st.ExecForTest(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	contact := func(email string) uuid.UUID {
		t.Helper()
		id, err := st.InsertContact(ctx, store.NewContact{BrandID: b.ID, Email: email, Status: store.ContactSubscribed, ConsentBasis: "single_opt_in"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	// campaign makes a campaign with a send to each contact, in the given
	// state, finished (if it's done) the given time ago.
	campaign := func(state string, finishedAgo time.Duration, contacts ...uuid.UUID) (uuid.UUID, []int64) {
		t.Helper()
		var sends []int64
		for _, c := range contacts {
			id, err := st.CreateSendForTest(ctx, b.ID, c)
			if err != nil {
				t.Fatal(err)
			}
			sends = append(sends, id)
		}
		ref, err := st.SendByID(ctx, sends[0])
		if err != nil {
			t.Fatal(err)
		}
		// Move the other sends into the first send's campaign.
		exec(`UPDATE sends SET campaign_id = $1 WHERE id = ANY($2)`, ref.CampaignID, sends[1:])
		exec(`UPDATE campaigns SET state = $2, html = '<p>Hi</p>', start_at = now() - $3::interval, confirmed_at = now() - $3::interval,
			finished_at = CASE WHEN $2 = 'done' THEN now() - $3::interval END WHERE id = $1`, ref.CampaignID, state, finishedAgo.String())
		return ref.CampaignID, sends
	}
	event := func(send *int64, typ, subtype string, ago time.Duration) {
		t.Helper()
		exec(`INSERT INTO email_events (event_id, type, subtype, brand_id, ses_message_id, send_id, occurred_at)
			VALUES (gen_random_uuid()::text, $1, $2, $3, gen_random_uuid()::text, $4, now() - $5::interval)`, typ, subtype, b.ID, send, ago.String())
	}
	const old, recent = 14 * 30 * 24 * time.Hour, 30 * 24 * time.Hour

	gmail, outlook := contact("ann@gmail.com"), contact("ben@outlook.com")
	done, ds := campaign("done", old, gmail, outlook)
	event(&ds[0], "delivery", "", old)
	event(&ds[0], "complaint", "abuse", old)
	event(&ds[1], "bounce", "permanent/General", old)
	event(&ds[1], "bounce", "transient/MailboxFull", old)
	event(&ds[1], "delivery_delay", "", old)
	_, rs := campaign("done", recent, contact("cat@example.com"))
	event(&rs[0], "delivery", "", recent)
	_, ss := campaign("sending", old, contact("dan@example.com")) // never finished: keeps its events
	event(&ss[0], "delivery", "", old)
	event(nil, "bounce", "permanent/General", old)    // not one of ours: goes
	event(nil, "bounce", "permanent/General", recent) // stays

	report := func() (store.FeedbackCounts, store.Engagement, []store.ProviderStat) {
		t.Helper()
		f, err := st.CampaignFeedback(ctx, done)
		if err != nil {
			t.Fatal(err)
		}
		e, err := st.CampaignEngagement(ctx, done)
		if err != nil {
			t.Fatal(err)
		}
		p, err := st.CampaignProviders(ctx, done)
		if err != nil {
			t.Fatal(err)
		}
		return f, e, p
	}
	f0, e0, p0 := report()
	if f0.Delivered != 1 || f0.Bounces != 1 || f0.Complaints != 1 || e0.SoftBounces != 1 || e0.DeliveryDelay != 1 || len(p0) != 2 {
		t.Fatalf("before: %+v %+v %+v", f0, e0, p0)
	}

	res, err := st.ApplyEventRetention(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.CampaignsSaved != 1 || res.EventsDeleted != 6 {
		t.Fatalf("result: %+v", res)
	}
	var left int
	if err := st.QueryIntForTest(ctx, `SELECT count(*) FROM email_events`, &left); err != nil {
		t.Fatal(err)
	}
	if left != 3 {
		t.Fatalf("%d events left, want 3 (recent campaign, unfinished campaign, recent orphan)", left)
	}
	if f, e, p := report(); f != f0 || !reflect.DeepEqual(e, e0) || !reflect.DeepEqual(p, p0) {
		t.Fatalf("report changed:\n%+v %+v %+v\n%+v %+v %+v", f0, e0, p0, f, e, p)
	}

	// Nothing more to do on the next run.
	if res, err := st.ApplyEventRetention(ctx, time.Now()); err != nil || res != (store.RetentionResult{}) {
		t.Fatalf("second run: %+v %v", res, err)
	}
}

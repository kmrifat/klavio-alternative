package app_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/campaigns"
	"github.com/kmrifat/klavio-alternative/api/internal/sending"
	"github.com/kmrifat/klavio-alternative/api/internal/ses"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// Regression tests for the worker: each case was first reproduced by a
// probe against the sending loop.

func (w *campaignWorld) schedule(id string, confirm int) {
	w.t.Helper()
	w.owner.must(200, w.owner.do("POST", campaignBase+"/campaigns/"+id+"/schedule", map[string]any{"version": 1, "confirm": fmt.Sprint(confirm)}))
}

func (w *campaignWorld) campaign(id string) store.Campaign {
	w.t.Helper()
	c, err := w.a.Store.CampaignByID(context.Background(), w.brand.ID, uuid.MustParse(id))
	if err != nil {
		w.t.Fatal(err)
	}
	return c
}

// step makes everything due, then runs one step.
func (w *campaignWorld) step(id string) {
	w.t.Helper()
	ctx := context.Background()
	if err := w.a.Store.ForceCampaignDueForTest(ctx, uuid.MustParse(id)); err != nil {
		w.t.Fatal(err)
	}
	if err := w.a.Campaigns.Step(ctx, uuid.MustParse(id)); err != nil {
		w.t.Fatal(err)
	}
}

func (w *campaignWorld) exec(sql string, args ...any) {
	w.t.Helper()
	if err := w.a.Store.ExecForTest(context.Background(), sql, args...); err != nil {
		w.t.Fatal(err)
	}
}

func (w *campaignWorld) count(sql string, args ...any) int64 {
	w.t.Helper()
	var n int64
	if err := w.a.Store.QueryRowForTest(context.Background(), sql, &n, args...); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// setBrandStatus is what the daily DNS check does when a record disappears.
func (w *campaignWorld) setBrandStatus(s store.BrandStatus) {
	w.t.Helper()
	w.exec(`UPDATE brands SET status = $2 WHERE id = $1`, w.brand.ID, string(s))
}

func recipients(msgs []ses.Message) map[string]int {
	seen := map[string]int{}
	for _, m := range msgs {
		seen[m.To]++
	}
	return seen
}

// A brand that stops checking out mid-campaign (DNS record removed, so the
// daily check moves it to setup) stops sending within one email, and the
// campaign pauses with a reason.
func TestCampaignStopsWhenBrandCantSend(t *testing.T) {
	w := newCampaignWorld(t)
	for i := range 30 {
		w.contact(fmt.Sprintf("b%02d@example.com", i), store.ContactSubscribed, 24*time.Hour, true)
	}
	id := w.draft(nil)["id"].(string)
	w.schedule(id, 30)
	w.runUntil(id, store.CampaignSending)
	before := len(w.sender.Messages())

	calls := 0
	w.hooks.set(func(ses.Message) error {
		if calls++; calls == 3 {
			w.setBrandStatus(store.BrandSetup)
		}
		return nil
	})
	w.step(id)
	if n := len(w.sender.Messages()) - before; n != 3 {
		t.Fatalf("sent %d after the brand stopped being ready, want exactly the 3 before it", n)
	}
	c := w.campaign(id)
	if c.State != store.CampaignPaused || c.WaitingReason != campaigns.WaitingBrandNotReady {
		t.Fatalf("campaign %s, waiting %q", c.State, c.WaitingReason)
	}
	for range 3 {
		w.step(id)
	}
	if n := len(w.sender.Messages()) - before; n != 3 {
		t.Fatalf("a campaign paused for its brand kept sending: %d", n)
	}

	// Fixed and resumed: the rest go out, nobody twice.
	w.hooks.set(nil)
	w.setBrandStatus(store.BrandReady)
	w.owner.must(200, w.owner.do("POST", campaignBase+"/campaigns/"+id+"/resume", nil))
	if c := w.campaign(id); c.WaitingReason != "" {
		t.Fatalf("waiting reason kept after resume: %q", c.WaitingReason)
	}
	w.runUntil(id, store.CampaignDone)
	seen := recipients(w.sender.Messages())
	for to, n := range seen {
		if n > 1 {
			t.Errorf("%s got %d copies", to, n)
		}
	}
	if len(seen) != 30 {
		t.Fatalf("%d people got it, want 30", len(seen))
	}

	// The same gate at the end of the hold.
	id2 := w.draft(map[string]any{"name": "Second"})["id"].(string)
	w.schedule(id2, 30)
	w.runUntil(id2, store.CampaignHolding)
	sent := len(w.sender.Messages())
	w.setBrandStatus(store.BrandSetup)
	w.step(id2)
	if c := w.campaign(id2); c.State != store.CampaignPaused || c.WaitingReason != campaigns.WaitingBrandNotReady {
		t.Fatalf("hold end with the brand not ready: %s %q", c.State, c.WaitingReason)
	}
	if len(w.sender.Messages()) != sent {
		t.Fatal("sent after the hold with the brand not ready")
	}
}

// A campaign that starts while its brand can't send pauses, but its test
// slice is still sent first after resume: it never moves to the full send
// without one.
func TestCampaignTestSliceSurvivesStartPause(t *testing.T) {
	w := newCampaignWorld(t)
	ctx := context.Background()
	for i := range 20 {
		w.contact(fmt.Sprintf("t%02d@example.com", i), store.ContactSubscribed, 24*time.Hour, true)
	}
	id := w.draft(nil)["id"].(string)
	w.schedule(id, 20)
	w.setBrandStatus(store.BrandSetup)
	w.step(id)
	c := w.campaign(id)
	if c.State != store.CampaignPaused || c.WaitingReason != campaigns.WaitingBrandNotReady {
		t.Fatalf("start with the brand not ready: %s %q", c.State, c.WaitingReason)
	}
	if n, err := w.a.Store.CountStageSends(ctx, c.ID, "test"); err != nil || n != 1 {
		t.Fatalf("test slice queued before pausing: %d %v", n, err)
	}
	if len(w.sender.Messages()) != 0 {
		t.Fatal("sent while the brand wasn't ready")
	}
	w.setBrandStatus(store.BrandReady)
	w.owner.must(200, w.owner.do("POST", campaignBase+"/campaigns/"+id+"/resume", nil))
	held := w.runUntil(id, store.CampaignHolding)
	progress, err := w.a.Store.SendProgress(ctx, held.ID)
	if err != nil {
		t.Fatal(err)
	}
	if progress["test"]["sent"] != 1 || len(w.sender.Messages()) != 1 {
		t.Fatalf("holding without the test slice sent: %v", progress)
	}

	// A test slice with no send rows at all (as a campaign paused by an
	// older worker left it) is queued and sent before holding.
	id2 := w.draft(map[string]any{"name": "Second"})["id"].(string)
	w.schedule(id2, 20)
	w.step(id2)
	if c := w.campaign(id2); c.State != store.CampaignTestSlice {
		t.Fatalf("second campaign: %s", c.State)
	}
	w.exec(`DELETE FROM sends WHERE campaign_id = $1 AND stage = 'test'`, uuid.MustParse(id2))
	held = w.runUntil(id2, store.CampaignHolding)
	if progress, _ := w.a.Store.SendProgress(ctx, held.ID); progress["test"]["sent"] != 1 {
		t.Fatalf("holding without the test slice sent: %v", progress)
	}
}

// When SES may have taken a message (a timeout, a reset connection), it is
// never sent again.
func TestCampaignAmbiguousSendNotRetried(t *testing.T) {
	w := newCampaignWorld(t)
	ctx := context.Background()
	w.contact("flaky@example.com", store.ContactSubscribed, 24*time.Hour, true)
	for i := range 4 {
		w.contact(fmt.Sprintf("a%d@example.com", i), store.ContactSubscribed, 24*time.Hour, true)
	}
	id := w.draft(nil)["id"].(string)
	w.schedule(id, 5)
	done := w.runUntil(id, store.CampaignDone)
	for range 3 {
		w.step(id)
		if err := w.a.Campaigns.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	seen := recipients(w.sender.Messages())
	if seen["flaky@example.com"] != 1 || len(seen) != 5 {
		t.Fatalf("recipients %v: flaky@ must get exactly one", seen)
	}
	if n := w.count(`SELECT count(*) FROM sends s JOIN contacts c ON c.id = s.contact_id
		WHERE s.campaign_id = $1 AND c.email = 'flaky@example.com' AND s.status = 'unknown'`, done.ID); n != 1 {
		t.Fatal("an ambiguous send should be recorded as unknown")
	}
}

// One step at a time per campaign, a step that runs out of time puts its
// claimed sends back, and a worker that died mid-send leaves the sends it
// never handed to SES for the queue and the rest as unknown.
func TestCampaignStepLeaseAndDeadline(t *testing.T) {
	w := newCampaignWorld(t)
	ctx := context.Background()
	for i := range 10 {
		w.contact(fmt.Sprintf("l%d@example.com", i), store.ContactSubscribed, 24*time.Hour, true)
	}
	id := w.draft(nil)["id"].(string)
	cid := uuid.MustParse(id)
	w.schedule(id, 10)

	// Someone holds the lease: the step leaves it alone.
	w.exec(`UPDATE campaigns SET step_lease_until = now() + interval '1 hour' WHERE id = $1`, cid)
	w.step(id)
	if c := w.campaign(id); c.State != store.CampaignScheduled {
		t.Fatalf("stepped while another worker held the lease: %s", c.State)
	}
	w.exec(`UPDATE campaigns SET step_lease_until = NULL WHERE id = $1`, cid)
	w.step(id)
	if c := w.campaign(id); c.State != store.CampaignTestSlice {
		t.Fatalf("after the lease: %s", c.State)
	}

	// A step already queued: the sweep doesn't start a second chain.
	w.exec(`INSERT INTO river_job (kind, args, max_attempts, state) VALUES ('campaigns.step', jsonb_build_object('campaign_id', $1::text), 5, 'scheduled')`, id)
	if err := w.a.Store.ForceCampaignDueForTest(ctx, cid); err != nil {
		t.Fatal(err)
	}
	if err := w.a.Campaigns.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if len(w.sender.Messages()) != 0 {
		t.Fatal("the sweep stepped a campaign that had a step queued")
	}
	w.exec(`DELETE FROM river_job WHERE kind = 'campaigns.step'`)
	if err := w.a.Campaigns.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if len(w.sender.Messages()) != 1 {
		t.Fatalf("the sweep should restart a lost chain: %d sent", len(w.sender.Messages()))
	}
	w.runUntil(id, store.CampaignSending)

	// Too little time left: nothing is sent and nothing stays claimed.
	if err := w.a.Store.ForceCampaignDueForTest(ctx, cid); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := w.a.Campaigns.Step(short, cid)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if len(w.sender.Messages()) != 1 {
		t.Fatal("sent with no time left in the step")
	}
	if n := w.count(`SELECT count(*) FROM sends WHERE campaign_id = $1 AND status = 'sending'`, cid); n != 0 {
		t.Fatalf("%d sends left claimed", n)
	}

	// A dead worker: one claimed send never reached SES, one did.
	var never, called int64
	if err := w.a.Store.QueryRowForTest(ctx, `SELECT id FROM sends WHERE campaign_id = $1 AND stage = 'rest' AND status = 'queued' ORDER BY id LIMIT 1`, &never, cid); err != nil {
		t.Fatal(err)
	}
	if err := w.a.Store.QueryRowForTest(ctx, `SELECT id FROM sends WHERE campaign_id = $1 AND stage = 'rest' AND status = 'queued' ORDER BY id OFFSET 1 LIMIT 1`, &called, cid); err != nil {
		t.Fatal(err)
	}
	w.exec(`UPDATE sends SET status = 'sending', attempts = 1, claimed_at = now() - interval '1 hour' WHERE id = $1`, never)
	w.exec(`UPDATE sends SET status = 'sending', attempts = 1, claimed_at = now() - interval '1 hour', called_at = now() - interval '1 hour' WHERE id = $1`, called)
	done := w.runUntil(id, store.CampaignDone)
	var neverTo, calledTo string
	_ = w.a.Store.QueryRowForTest(ctx, `SELECT c.email::text FROM sends s JOIN contacts c ON c.id = s.contact_id WHERE s.id = $1`, &neverTo, never)
	_ = w.a.Store.QueryRowForTest(ctx, `SELECT c.email::text FROM sends s JOIN contacts c ON c.id = s.contact_id WHERE s.id = $1`, &calledTo, called)
	seen := recipients(w.sender.Messages())
	if seen[neverTo] != 1 || seen[calledTo] != 0 || len(seen) != 9 {
		t.Fatalf("after a dead worker: %v (never called %s, called %s)", seen, neverTo, calledTo)
	}
	if n := w.count(`SELECT count(*) FROM sends WHERE campaign_id = $1 AND status = 'unknown'`, done.ID); n != 1 {
		t.Fatalf("%d unknown sends, want 1", n)
	}
}

// Sends pushed back (warm-up allowance used up, SES throttling or briefly
// unavailable) still land outside the recipient's quiet hours.
func TestCampaignDeferralsRespectQuietHours(t *testing.T) {
	t.Run("warm-up overflow", func(t *testing.T) {
		w := newCampaignWorld(t)
		w.a.Campaigns.SetWarmup(&fakeWarmup{daily: 1, used: map[string]int{}})
		for i := range 4 {
			w.contact(fmt.Sprintf("q%d@example.com", i), store.ContactSubscribed, 24*time.Hour, true)
		}
		// Midnight UTC is 01:00 or 02:00 in Berlin: inside quiet hours.
		w.exec(`UPDATE contacts SET timezone = 'Europe/Berlin' WHERE brand_id = $1`, w.brand.ID)
		id := w.draft(nil)["id"].(string)
		w.schedule(id, 4)
		w.runUntil(id, store.CampaignSending)
		w.step(id)
		want := sending.SendAfter(time.Now().UTC().Truncate(24*time.Hour).Add(24*time.Hour), "Europe/Berlin")
		cid := uuid.MustParse(id)
		if n := w.count(`SELECT count(*) FROM sends WHERE campaign_id = $1 AND status = 'queued'`, cid); n == 0 {
			t.Fatal("nothing was deferred")
		}
		if n := w.count(`SELECT count(*) FROM sends WHERE campaign_id = $1 AND status = 'queued' AND send_after <> $2`, cid, want); n != 0 {
			t.Fatalf("%d deferred sends not at the recipient's next morning (%s)", n, want)
		}
	})

	t.Run("retries", func(t *testing.T) {
		w := newCampaignWorld(t)
		ctx := context.Background()
		zone := quietZone(t, time.Now().Add(time.Minute))
		loc, _ := time.LoadLocation(zone)
		w.contact("unavailable@example.com", store.ContactSubscribed, 24*time.Hour, true)
		w.contact("throttle@example.com", store.ContactSubscribed, 24*time.Hour, true)
		w.exec(`UPDATE contacts SET timezone = $2 WHERE brand_id = $1`, w.brand.ID, zone)
		failed := false
		w.hooks.set(func(m ses.Message) error {
			if strings.HasPrefix(m.To, "unavailable@") && !failed {
				failed = true
				return fmt.Errorf("%w: fake 503", ses.ErrUnavailable)
			}
			return nil
		})
		id := w.draft(nil)["id"].(string)
		cid := uuid.MustParse(id)
		w.schedule(id, 2)
		retryAt := map[string]time.Time{}
		for range 20 {
			if len(retryAt) == 2 {
				break
			}
			w.step(id)
			for _, email := range []string{"unavailable@example.com", "throttle@example.com"} {
				if _, ok := retryAt[email]; ok {
					continue
				}
				var at time.Time
				if err := w.a.Store.QueryRowForTest(ctx, `SELECT coalesce(max(s.send_after), 'epoch'::timestamptz) FROM sends s JOIN contacts c ON c.id = s.contact_id
					WHERE s.campaign_id = $1 AND c.email = $2 AND s.status = 'queued' AND s.error <> ''`, &at, cid, email); err != nil {
					t.Fatal(err)
				}
				if at.Unix() != 0 {
					retryAt[email] = at
				}
			}
		}
		if len(retryAt) != 2 {
			t.Fatalf("retries seen: %v", retryAt)
		}
		for email, at := range retryAt {
			local := at.In(loc)
			if local.Hour() != sending.QuietTo || local.Minute() != 0 || at.Before(time.Now().Add(time.Hour)) {
				t.Errorf("%s retried at %s (%s local), want the next %02d:00 in %s", email, at, local, sending.QuietTo, zone)
			}
		}
	})
}

// quietZone picks a time zone where `at` is inside quiet hours with at
// least an hour of them left.
func quietZone(t *testing.T, at time.Time) string {
	t.Helper()
	for _, z := range []string{"Pacific/Kiritimati", "Pacific/Auckland", "Asia/Tokyo", "Asia/Kolkata", "Asia/Dubai",
		"Europe/Berlin", "UTC", "America/Sao_Paulo", "America/New_York", "America/Chicago", "America/Los_Angeles", "Pacific/Honolulu"} {
		if sending.SendAfter(at, z).After(at.Add(time.Hour)) {
			return z
		}
	}
	t.Fatal("no zone in quiet hours")
	return ""
}

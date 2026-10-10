package app_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/ses"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// reservations checks that the warm-up and the frequency cap count exactly
// the emails that went out (or may have), and that nothing is left waiting.
func (w *campaignWorld) checkReservations(cid uuid.UUID) {
	w.t.Helper()
	ctx := context.Background()
	out := w.count(`SELECT count(*) FROM sends WHERE campaign_id = $1 AND status IN ('sent', 'unknown')`, cid)
	warm, err := w.a.Store.SentOn(ctx, w.brand.ID, time.Now().UTC().Truncate(24*time.Hour))
	if err != nil {
		w.t.Fatal(err)
	}
	caps := w.count(`SELECT count(*) FROM marketing_sends WHERE brand_id = $1`, w.brand.ID)
	if int64(warm) != out || caps != out {
		w.t.Errorf("warm-up counts %d, frequency cap %d, but %d went out", warm, caps, out)
	}
	if n := w.count(`SELECT count(*) FROM sends WHERE campaign_id = $1 AND status IN ('queued', 'sending')`, cid); n != 0 {
		w.t.Errorf("%d sends still waiting in a cancelled campaign", n)
	}
	if n := w.count(`SELECT count(*) FROM sends WHERE campaign_id = $1 AND reserved_at IS NOT NULL AND status NOT IN ('sent', 'unknown')`, cid); n != 0 {
		w.t.Errorf("%d unsent sends still hold a reservation", n)
	}
}

// Cancelling while a batch is going out: the batch's unsent sends are
// skipped and give back their warm-up and frequency-cap room.
func TestCampaignCancelMidBatchGivesBack(t *testing.T) {
	w := newCampaignWorld(t)
	w.a.Campaigns.SetFreqCap(w.a.FreqCap)
	for i := range 40 {
		w.contact(fmt.Sprintf("m%02d@example.com", i), store.ContactSubscribed, 24*time.Hour, true)
	}
	id := w.draft(nil)["id"].(string)
	cid := uuid.MustParse(id)
	w.schedule(id, 40)
	w.runUntil(id, store.CampaignSending)

	calls := 0
	w.hooks.set(func(m ses.Message) error {
		if calls++; calls == 3 {
			w.owner.must(200, w.owner.do("POST", campaignBase+"/campaigns/"+id+"/cancel", nil))
		}
		return nil
	})
	w.step(id)
	if c := w.campaign(id); c.State != store.CampaignCancelled {
		t.Fatalf("campaign %s", c.State)
	}
	if calls != 3 {
		t.Fatalf("%d SES calls in the batch, want it to stop after the cancel", calls)
	}
	w.checkReservations(cid)

	// A throttled send whose retry would land after a cancel: same.
	id2 := w.draft(map[string]any{"name": "Second"})["id"].(string)
	cid2 := uuid.MustParse(id2)
	w.a.Campaigns.SetFreqCap(nil) // the same 40 people again, seconds later
	w.schedule(id2, 40)
	w.runUntil(id2, store.CampaignSending)
	w.a.Campaigns.SetFreqCap(w.a.FreqCap)
	w.exec(`DELETE FROM marketing_sends`)
	w.exec(`DELETE FROM brand_daily_sends`)
	calls = 0
	w.hooks.set(func(m ses.Message) error {
		if calls++; calls == 2 {
			w.owner.must(200, w.owner.do("POST", campaignBase+"/campaigns/"+id2+"/cancel", nil))
			return fmt.Errorf("%w: fake", ses.ErrThrottled)
		}
		return nil
	})
	w.step(id2)
	if c := w.campaign(id2); c.State != store.CampaignCancelled {
		t.Fatalf("second campaign %s", c.State)
	}
	// Only this campaign's sends count now (the counters were reset).
	out := w.count(`SELECT count(*) FROM sends WHERE campaign_id = $1 AND status IN ('sent', 'unknown') AND stage = 'rest'`, cid2)
	if caps := w.count(`SELECT count(*) FROM marketing_sends`); caps != out {
		t.Errorf("frequency cap counts %d, %d went out", caps, out)
	}
	if n := w.count(`SELECT count(*) FROM sends WHERE campaign_id = $1 AND status IN ('queued', 'sending')`, cid2); n != 0 {
		t.Errorf("%d sends still waiting", n)
	}
}

// A worker that crashed after a cancel left sends queued, or claimed and
// never sent, still holding their room: the sweep ends them and gives it
// back.
func TestCampaignSweepCleansLeftovers(t *testing.T) {
	w := newCampaignWorld(t)
	ctx := context.Background()
	w.a.Campaigns.SetFreqCap(w.a.FreqCap)
	for i := range 20 {
		w.contact(fmt.Sprintf("x%02d@example.com", i), store.ContactSubscribed, 24*time.Hour, true)
	}
	id := w.draft(nil)["id"].(string)
	cid := uuid.MustParse(id)
	w.schedule(id, 20)
	w.runUntil(id, store.CampaignSending)
	// SES pauses: the batch goes back to the queue, still reserved.
	w.hooks.set(func(ses.Message) error { return fmt.Errorf("%w: fake", ses.ErrSendingPaused) })
	w.step(id)
	if n := w.count(`SELECT count(*) FROM sends WHERE campaign_id = $1 AND status = 'queued' AND reserved_at IS NOT NULL`, cid); n == 0 {
		t.Fatal("expected reserved sends back in the queue")
	}
	// The crash: cancelled without the give-back, one send claimed and
	// abandoned before SES was called.
	w.exec(`UPDATE campaigns SET state = 'cancelled', paused_from = NULL WHERE id = $1`, cid)
	w.exec(`UPDATE sends SET status = 'sending', called_at = NULL, claimed_at = now() - interval '1 hour'
		WHERE id = (SELECT min(id) FROM sends WHERE campaign_id = $1 AND status = 'queued')`, cid)
	if err := w.a.Campaigns.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	w.checkReservations(cid)
}

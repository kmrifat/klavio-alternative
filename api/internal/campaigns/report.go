package campaigns

import (
	"context"
	"net/http"
	"time"

	"github.com/kmrifat/klavio-alternative/api/internal/brakes"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/limits"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// Live monitor (S7) and campaign report (S11), M4.

type rates struct {
	Bounce    float64 `json:"bounce"`
	Complaint float64 `json:"complaint"`
	Unsub     float64 `json:"unsubscribe"`
}

// Monitor is S7: polled every few seconds while a campaign sends.
type Monitor struct {
	Campaign   store.Campaign      `json:"campaign"`
	Sends      store.SendCounts    `json:"sends"`
	Feedback   brakes.Counts       `json:"feedback"`
	Rates      rates               `json:"rates"`  // fractions
	Limits     rates               `json:"limits"` // the campaign brake, as fractions
	Engagement store.Engagement    `json:"engagement"`
	Gate       *brakes.Gate        `json:"gate"`
	Events     []store.SafetyEvent `json:"safety_events"`
	At         time.Time           `json:"at"`
}

// Report is S11: the monitor plus links and providers.
type Report struct {
	Monitor
	Links     []store.LinkStat     `json:"links"`
	Providers []store.ProviderStat `json:"providers"`
}

func (s *Service) monitor(ctx context.Context, c store.Campaign) (Monitor, error) {
	m := Monitor{Campaign: c, At: s.now()}
	var err error
	if m.Sends, err = s.store.CampaignSendCounts(ctx, c.ID); err != nil {
		return m, err
	}
	f, err := s.store.CampaignFeedback(ctx, c.ID)
	if err != nil {
		return m, err
	}
	m.Feedback = brakes.Counts{Delivered: f.Delivered, Bounces: f.Bounces, Complaints: f.Complaints, Unsubscribes: f.Unsubscribes}
	m.Rates = rates{Bounce: m.Feedback.BounceRate(), Complaint: m.Feedback.ComplaintRate(), Unsub: m.Feedback.UnsubRate()}
	m.Limits = rates{
		Bounce:    s.limits.Get(ctx, limits.CampaignBouncePct) / 100,
		Complaint: s.limits.Get(ctx, limits.CampaignComplaintPct) / 100,
		Unsub:     s.limits.Get(ctx, limits.CampaignUnsubPct) / 100,
	}
	if m.Engagement, err = s.store.CampaignEngagement(ctx, c.ID); err != nil {
		return m, err
	}
	if s.brakes != nil {
		g, err := s.brakes.CheckGate(ctx, nil, c.BrandID)
		if err != nil {
			return m, err
		}
		m.Gate = &g
	}
	if m.Events, err = s.store.CampaignSafetyEvents(ctx, c.ID); err != nil {
		return m, err
	}
	if m.Events == nil {
		m.Events = []store.SafetyEvent{}
	}
	return m, nil
}

func (s *Service) monitorHandler(w http.ResponseWriter, r *http.Request) error {
	_, _, c, err := s.campaign(r, store.RoleViewer)
	if err != nil {
		return err
	}
	m, err := s.monitor(r.Context(), c)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, m)
	return nil
}

func (s *Service) reportHandler(w http.ResponseWriter, r *http.Request) error {
	_, _, c, err := s.campaign(r, store.RoleViewer)
	if err != nil {
		return err
	}
	ctx := r.Context()
	m, err := s.monitor(ctx, c)
	if err != nil {
		return err
	}
	rep := Report{Monitor: m}
	if rep.Links, err = s.store.CampaignLinks(ctx, c.ID, 20); err != nil {
		return err
	}
	if rep.Providers, err = s.store.CampaignProviders(ctx, c.ID); err != nil {
		return err
	}
	if rep.Links == nil {
		rep.Links = []store.LinkStat{}
	}
	if rep.Providers == nil {
		rep.Providers = []store.ProviderStat{}
	}
	httpx.WriteJSON(w, http.StatusOK, rep)
	return nil
}

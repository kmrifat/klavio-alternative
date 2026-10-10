package brakes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/health"
	"github.com/kmrifat/klavio-alternative/api/internal/limits"
	"github.com/kmrifat/klavio-alternative/api/internal/ses"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// Window: brand and account brakes look at the last 7 days.
const Window = 7 * 24 * time.Hour

// gmailFresh: Postmaster data older than this says nothing about now.
const gmailFresh = 3 * 24 * time.Hour

type Service struct {
	store *store.Store
	ses   ses.Provisioner
	log   *slog.Logger
	now   func() time.Time
}

func NewService(st *store.Store, prov ses.Provisioner, log *slog.Logger) *Service {
	return &Service{store: st, ses: prov, log: log, now: time.Now}
}

func counts(f store.FeedbackCounts) Counts {
	return Counts{Delivered: f.Delivered, Bounces: f.Bounces, Complaints: f.Complaints, Unsubscribes: f.Unsubscribes}
}

// Reputation is the brand's own sending for the health score (health.ReputationSource).
func (s *Service) Reputation(ctx context.Context, brandID uuid.UUID) (*health.ReputationSignals, error) {
	f, err := s.store.FeedbackCounts(ctx, &brandID, s.now().Add(-Window))
	if err != nil {
		return nil, err
	}
	c := counts(f)
	return &health.ReputationSignals{Sent7d: c.Attempts(), BounceRate7d: c.BounceRate(), ComplaintRate7d: c.ComplaintRate()}, nil
}

// Evaluate is the periodic job: the account first, then every brand.
func (s *Service) Evaluate(ctx context.Context) error {
	errs := []error{s.EvaluateAccount(ctx)}
	brands, err := s.store.ListBrands(ctx)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, b := range brands {
		if err := s.EvaluateBrand(ctx, b.ID); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", b.Slug, err))
		}
	}
	return errors.Join(errs...)
}

// EvaluateBrand tightens the brand's brake when its last 7 days call for
// it. It never loosens one (§6.4: loosen only with a second admin): that
// goes through the recovery flow (Recovery), whatever the rates say, since
// a brand that stops sending soon has too few emails to judge.
func (s *Service) EvaluateBrand(ctx context.Context, brandID uuid.UUID) error {
	now := s.now()
	return s.store.InTx(ctx, func(tx *store.Store) error {
		// Everything through tx. A run already evaluating this brand wins.
		if ok, err := tx.TryLock(ctx, "brakes:brand:"+brandID.String()); err != nil || !ok {
			return err
		}
		b, err := tx.BrandByID(ctx, brandID)
		if err != nil {
			return err
		}
		current := State(b.BrakeState)
		if current == Paused {
			return nil
		}
		f, err := tx.FeedbackCounts(ctx, &b.ID, now.Add(-Window))
		if err != nil {
			return err
		}
		var gmail *float64
		if p, err := tx.LatestPostmaster(ctx, b.ID); err == nil && now.Sub(p.Day) < gmailFresh {
			gmail = p.SpamRatio
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		l := BrandLimits{PauseComplaintPct: 0.08}
		if l.PauseBouncePct, err = limits.Read(ctx, tx, limits.BrandBouncePct7d); err != nil {
			return err
		}
		if l.GmailSpamPct, err = limits.Read(ctx, tx, limits.BrandGmailSpamPct); err != nil {
			return err
		}
		c := counts(f)
		target, reasons := Brand(c, gmail, l)
		if !Worse(target, current) {
			return nil // only tighten
		}
		action := string(target)
		if err := tx.SetBrakeState(ctx, b.ID, string(target)); err != nil {
			return err
		}
		reason := strings.Join(reasons, "; ")
		metrics := map[string]any{"counts": c, "bounce_rate": c.BounceRate(), "complaint_rate": c.ComplaintRate(), "gmail_spam": gmail}
		if err := tx.AddSafetyEvent(ctx, store.SafetyEvent{Scope: "brand", BrandID: &b.ID, Action: action,
			FromState: string(current), ToState: string(target), Reason: reason}, metrics); err != nil {
			return err
		}
		s.log.WarnContext(ctx, "brand brake changed", "brand", b.Slug, "from", current, "to", target, "reason", reason)
		if target == Paused || target == Slowed {
			sev := map[State]string{Paused: "critical", Slowed: "warning"}[target]
			msg := fmt.Sprintf("%s is %s: %s", b.Name, map[State]string{Paused: "paused", Slowed: "slowed down"}[target], reason)
			return tx.CreateAlert(ctx, store.Alert{Kind: "brand_" + string(target), Severity: sev, BrandID: &b.ID, Message: msg}, metrics)
		}
		return nil
	})
}

// EvaluateAccount stops all sending when complaints across brands, or
// Amazon, say so. It never restarts on its own.
func (s *Service) EvaluateAccount(ctx context.Context) error {
	if at, _, err := s.store.AccountBrake(ctx); err != nil || at != nil {
		return err
	}
	f, err := s.store.FeedbackCounts(ctx, nil, s.now().Add(-Window))
	if err != nil {
		return err
	}
	pctLimit, err := limits.Read(ctx, s.store, limits.AccountComplaintPct)
	if err != nil {
		return err
	}
	enabled, enforcement := true, ""
	if acc, err := s.ses.Account(ctx); err == nil {
		enabled, enforcement = acc.SendingEnabled, acc.EnforcementStatus
	} else {
		s.log.WarnContext(ctx, "SES account for the account brake", "err", err)
	}
	c := counts(f)
	stop, reasons := Account(c, pctLimit, enabled, enforcement)
	if !stop {
		return nil
	}
	reason := strings.Join(reasons, "; ")
	return s.store.InTx(ctx, func(tx *store.Store) error {
		fresh, err := tx.SetAccountBrake(ctx, reason)
		if err != nil || !fresh {
			return err
		}
		metrics := map[string]any{"counts": c, "complaint_rate": c.ComplaintRate(), "sending_enabled": enabled, "enforcement": enforcement}
		if err := tx.AddSafetyEvent(ctx, store.SafetyEvent{Scope: "account", Action: "stopped", ToState: "stopped", Reason: reason}, metrics); err != nil {
			return err
		}
		s.log.ErrorContext(ctx, "account brake: all sending stopped", "reason", reason)
		return tx.CreateAlert(ctx, store.Alert{Kind: "account_brake", Severity: "critical", Message: "All sending stopped: " + reason}, metrics)
	})
}

// Gate is what the sending worker checks before each batch.
type Gate struct {
	Allowed bool `json:"allowed"`
	// RateFactor scales the send rate: 0.5 while the brand is slowed.
	RateFactor float64 `json:"rate_factor"`
	// NeedsApproval: every send needs a second admin (slowed).
	NeedsApproval bool `json:"needs_approval"`
	// BlockInactive: leave out at-risk contacts even if the campaign included them (watch).
	BlockInactive bool   `json:"block_inactive"`
	Reason        string `json:"reason"`
}

// CheckGate: may the brand send right now, and how? Reads through tx (the
// worker's transaction; nil = the pool).
func (s *Service) CheckGate(ctx context.Context, tx *store.Store, brandID uuid.UUID) (Gate, error) {
	if tx == nil {
		tx = s.store
	}
	at, reason, err := tx.AccountBrake(ctx)
	if err != nil {
		return Gate{}, err
	}
	if at != nil {
		return Gate{Reason: "all sending is stopped: " + reason}, nil
	}
	b, err := tx.BrandByID(ctx, brandID)
	if err != nil {
		return Gate{}, err
	}
	if b.Status == store.BrandPaused {
		return Gate{Reason: b.Name + " is paused"}, nil
	}
	g := Gate{Allowed: true, RateFactor: 1}
	switch State(b.BrakeState) {
	case Paused:
		return Gate{Reason: b.Name + " is paused"}, nil
	case Slowed:
		g.RateFactor, g.NeedsApproval, g.BlockInactive = 0.5, true, true
		g.Reason = b.Name + " is slowed down by its brake"
	case Watch:
		g.BlockInactive, g.Reason = true, b.Name+" is on watch"
	}
	return g, nil
}

// CheckCampaign: should a running campaign pause (§6.4 campaign level)?
// Used after the test slice and between batches. A pause is recorded as a
// safety event; the caller pauses the campaign.
func (s *Service) CheckCampaign(ctx context.Context, c store.Campaign) (pause bool, reason string, err error) {
	f, err := s.store.CampaignFeedback(ctx, c.ID)
	if err != nil {
		return false, "", err
	}
	var l CampaignLimits
	for key, dst := range map[string]*float64{
		limits.CampaignBouncePct: &l.BouncePct, limits.CampaignComplaintPct: &l.ComplaintPct, limits.CampaignUnsubPct: &l.UnsubPct,
	} {
		if *dst, err = limits.Read(ctx, s.store, key); err != nil {
			return false, "", err
		}
	}
	cnt := counts(f)
	pause, reasons := Campaign(cnt, l)
	if !pause {
		return false, "", nil
	}
	reason = "Paused by the campaign brake: " + strings.Join(reasons, "; ")
	err = s.store.AddSafetyEvent(ctx, store.SafetyEvent{Scope: "campaign", BrandID: &c.BrandID, CampaignID: &c.ID, Action: "paused",
		FromState: string(c.State), ToState: "paused", Reason: strings.Join(reasons, "; ")},
		map[string]any{"counts": cnt, "bounce_rate": cnt.BounceRate(), "complaint_rate": cnt.ComplaintRate(), "unsub_rate": cnt.UnsubRate()})
	return true, reason, err
}

// AfterTestSlice is CheckCampaign at the end of the hold (campaigns.Brakes).
func (s *Service) AfterTestSlice(ctx context.Context, c store.Campaign) (bool, string, error) {
	return s.CheckCampaign(ctx, c)
}

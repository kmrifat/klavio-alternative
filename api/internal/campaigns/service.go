// Package campaigns runs a campaign from draft to done: who it goes to,
// the approvals it needs, the confirmation before sending (S6), and the
// sending worker's steps (test slice, hold, rest).
package campaigns

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"math"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/approvals"
	"github.com/kmrifat/klavio-alternative/api/internal/freqcap"
	"github.com/kmrifat/klavio-alternative/api/internal/sending"
	"github.com/kmrifat/klavio-alternative/api/internal/ses"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/templates"
	"github.com/kmrifat/klavio-alternative/api/internal/unsubscribe"
)

// CancelWindow: after a sender confirms, nothing goes out for this long, so
// a mistake can still be cancelled.
const CancelWindow = 10 * time.Minute

// Batch is how many sends one worker step claims.
const Batch = 50

// MaxAttempts per send before it fails for good (throttling doesn't count).
const MaxAttempts = 3

// Limits reads safety limits (M5's limits service). Defaults are used
// until it is wired in.
type Limits interface {
	Get(ctx context.Context, key string) float64
}

// Limit keys, matching M5's limits package.
const (
	LimitApprovalAbove = "approval_needed_above"
	LimitTestSlicePct  = "test_slice_pct"
	LimitHoldMinutes   = "test_slice_hold_minutes"
)

// DefaultLimits are PLAN.md's defaults.
type DefaultLimits struct{}

func (DefaultLimits) Get(_ context.Context, key string) float64 {
	return map[string]float64{LimitApprovalAbove: 10000, LimitTestSlicePct: 5, LimitHoldMinutes: 45}[key]
}

// Warmup limits a new brand's daily volume (M6). Until it is wired in,
// there is no daily cap.
type Warmup interface {
	// Reserve takes up to want sends from the brand's allowance for the
	// UTC day, atomically, inside tx, and returns how many were granted.
	Reserve(ctx context.Context, tx *store.Store, brandID uuid.UUID, day time.Time, want int) (int, error)
	// Release gives back n sends reserved for day that didn't go out.
	Release(ctx context.Context, tx *store.Store, brandID uuid.UUID, day time.Time, n int) error
	// InWarmup reports whether sends on day need a warm-up approval.
	InWarmup(ctx context.Context, brandID uuid.UUID, day time.Time) (bool, error)
}

// Brakes decides after the test slice whether the rest may go (M4 reads
// bounces and complaints). Until then every campaign continues.
type Brakes interface {
	AfterTestSlice(ctx context.Context, c store.Campaign) (pause bool, reason string, err error)
}

// FreqCap keeps one person from getting too much email across brands
// (M7's *freqcap.Service). Without one there is no cap.
type FreqCap interface {
	// Filter says who would be held back now (read-only; tx may be nil).
	Filter(ctx context.Context, tx *store.Store, brandID uuid.UUID, contactIDs []uuid.UUID, now time.Time) (allowed []uuid.UUID, excluded []freqcap.Excluded, err error)
	// Reserve is Filter plus recording a send at now for everyone allowed,
	// inside tx.
	Reserve(ctx context.Context, tx *store.Store, brandID uuid.UUID, contactIDs []uuid.UUID, now time.Time) (allowed []uuid.UUID, excluded []freqcap.Excluded, err error)
	// Release forgets sends reserved at `at` that didn't go out.
	Release(ctx context.Context, tx *store.Store, brandID uuid.UUID, contactIDs []uuid.UUID, at time.Time) error
}

// Runner queues the worker's next step (river jobs in production).
type Runner interface {
	QueueCampaignStep(ctx context.Context, campaignID uuid.UUID, at time.Time) error
}

type Service struct {
	store     *store.Store
	log       *slog.Logger
	sender    ses.Sender
	signer    *unsubscribe.Signer
	baseURL   string // PublicBaseURLOverride; "" = https://<brand link domain>
	limits    Limits
	approvals *approvals.Service
	warmup    Warmup
	freqcap   FreqCap
	brakes    Brakes
	runner    Runner
	pacer     *sending.Pacer
	rate      float64 // sends per second
	now       func() time.Time
}

type Config struct {
	Sender         ses.Sender
	Signer         *unsubscribe.Signer
	PublicBaseURL  string
	SendsPerSecond float64
}

func NewService(st *store.Store, cfg Config, log *slog.Logger) *Service {
	return &Service{
		store: st, log: log, sender: cfg.Sender, signer: cfg.Signer, baseURL: cfg.PublicBaseURL,
		limits: DefaultLimits{}, pacer: sending.NewPacer(cfg.SendsPerSecond), rate: max(cfg.SendsPerSecond, 1), now: time.Now,
	}
}

func (s *Service) SetRunner(r Runner)   { s.runner = r }
func (s *Service) SetLimits(l Limits)   { s.limits = l }
func (s *Service) SetWarmup(w Warmup)   { s.warmup = w }
func (s *Service) SetFreqCap(f FreqCap) { s.freqcap = f }
func (s *Service) SetBrakes(b Brakes)   { s.brakes = b }

func (s *Service) queue(ctx context.Context, id uuid.UUID, at time.Time) {
	if s.runner == nil {
		return
	}
	if err := s.runner.QueueCampaignStep(ctx, id, at); err != nil {
		// The periodic sweep picks it up.
		s.log.WarnContext(ctx, "queue campaign step", "campaign_id", id, "err", err)
	}
}

// --- approval rules (PLAN.md §6.1) ---

type Reason string

const (
	ReasonLarge       Reason = "large_audience"   // over the approval limit
	ReasonUnusual     Reason = "unusual_size"     // over twice the brand's usual audience
	ReasonAtRisk      Reason = "at_risk_included" // at-risk contacts opted in
	ReasonFirstImport Reason = "first_send_to_import"
	ReasonWarmup      Reason = "brand_warming_up" // the brand is still in its warm-up weeks
)

var reasonText = map[Reason]string{
	ReasonLarge:       "goes to more people than the approval limit",
	ReasonUnusual:     "is more than twice this brand's usual audience",
	ReasonAtRisk:      "includes at-risk contacts",
	ReasonFirstImport: "is the first email to recently imported contacts",
	ReasonWarmup:      "is sent while the brand is still warming up",
}

func (r Reason) Text() string { return reasonText[r] }

// ApprovalReasons says why a send needs a second person, if it does.
func ApprovalReasons(sum store.AudienceSummary, usual int, approvalAbove float64) []Reason {
	var out []Reason
	if float64(sum.Included) > approvalAbove {
		out = append(out, ReasonLarge)
	}
	if usual > 0 && sum.Included > 2*usual {
		out = append(out, ReasonUnusual)
	}
	if sum.AtRisk > 0 {
		out = append(out, ReasonAtRisk)
	}
	if sum.FirstTimeImported > 0 {
		out = append(out, ReasonFirstImport)
	}
	return out
}

// --- review and scheduling ---

// Review is what the sender sees on the confirmation screen (S6).
type Review struct {
	Audience        store.AudienceSummary `json:"audience"`
	ApprovalReasons []Reason              `json:"approval_reasons"`
	Problems        []string              `json:"problems"` // anything that blocks scheduling
	TestSlicePct    float64               `json:"test_slice_pct"`
	HoldMinutes     int                   `json:"hold_minutes"`
	FromHeader      string                `json:"from_header"`
	// ConfirmText is what the sender must type: the number of people.
	ConfirmText string `json:"confirm_text"`
}

// FromHeader builds `"Brand" <hello@news.brand.com>` for a brand.
func FromHeader(b store.Brand, sender store.BrandSender) string {
	name := sender.FromName
	if name == "" {
		name = b.Name
	}
	return (&mail.Address{Name: name, Address: sender.FromLocal + "@" + b.SendingDomain}).String()
}

// Review checks a draft and counts its audience without changing anything.
func (s *Service) Review(ctx context.Context, b store.Brand, c store.Campaign) (Review, store.Template, error) {
	var tpl store.Template
	rv := Review{
		TestSlicePct: s.limits.Get(ctx, LimitTestSlicePct),
		HoldMinutes:  int(s.limits.Get(ctx, LimitHoldMinutes)),
	}
	sum, err := s.audience(ctx, s.store, b.ID, c)
	if err != nil {
		return rv, tpl, err
	}
	rv.Audience = sum
	rv.ConfirmText = fmt.Sprint(sum.Included)
	usual, err := s.store.UsualAudienceSize(ctx, b.ID)
	if err != nil {
		return rv, tpl, err
	}
	rv.ApprovalReasons = ApprovalReasons(sum, usual, s.limits.Get(ctx, LimitApprovalAbove))
	if s.warmup != nil {
		warming, err := s.warmup.InWarmup(ctx, b.ID, s.now())
		if err != nil {
			return rv, tpl, err
		}
		if warming {
			rv.ApprovalReasons = append(rv.ApprovalReasons, ReasonWarmup)
		}
	}

	sender, err := s.store.BrandSender(ctx, b.ID)
	if err != nil {
		return rv, tpl, err
	}
	rv.FromHeader = FromHeader(b, sender)

	problem := func(msg string) { rv.Problems = append(rv.Problems, msg) }
	switch b.Status {
	case store.BrandPaused:
		problem("the brand is paused; nothing can be sent until it is resumed")
	case store.BrandSetup:
		problem("the brand's DNS isn't set up yet; sending is blocked until its records check out")
	}
	if !c.State.Editable() {
		problem("the campaign has already been scheduled")
	}
	if strings.TrimSpace(c.Subject) == "" {
		problem("add a subject line")
	}
	if sum.Included == 0 {
		problem("nobody in this audience can receive the email")
	}
	if c.TemplateID == nil {
		problem("choose a template")
	} else {
		tpl, err = s.store.TemplateByID(ctx, b.ID, *c.TemplateID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			problem("the template was archived; choose another")
		case err != nil:
			return rv, tpl, err
		case tpl.ErrorCount > 0:
			problem(fmt.Sprintf("the template %q has %d error(s) to fix first", tpl.Name, tpl.ErrorCount))
		case tpl.Stale:
			problem(fmt.Sprintf("the template %q is being re-checked after a layout change; try again in a minute", tpl.Name))
		}
	}
	if rv.Problems == nil {
		rv.Problems = []string{}
	}
	if rv.ApprovalReasons == nil {
		rv.ApprovalReasons = []Reason{}
	}
	return rv, tpl, nil
}

// ScheduleInput is the sender's confirmation.
type ScheduleInput struct {
	Version     int        `json:"version"`
	Confirm     string     `json:"confirm"`      // must equal Review.ConfirmText
	ScheduledAt *time.Time `json:"scheduled_at"` // nil = as soon as the cancel window allows
}

var (
	ErrNotEditable     = errors.New("campaign is not a draft")
	ErrProblems        = errors.New("campaign can't be scheduled yet")
	ErrConfirmMismatch = errors.New("confirmation doesn't match the audience")
	ErrNeedsApproval   = errors.New("needs approval, and nobody else can approve it")
)

// ProblemsError carries what blocks scheduling.
type ProblemsError struct{ Problems []string }

func (e *ProblemsError) Error() string { return strings.Join(e.Problems, "; ") }
func (e *ProblemsError) Unwrap() error { return ErrProblems }

// Schedule freezes the campaign (content, sender, audience snapshot) and
// either schedules it, after the cancel window, or asks for approval.
func (s *Service) Schedule(ctx context.Context, b store.Brand, c store.Campaign, in ScheduleInput, by store.User, audit store.AuditEntry) (store.Campaign, Review, error) {
	rv, tpl, err := s.Review(ctx, b, c)
	if err != nil {
		return c, rv, err
	}
	switch {
	case len(rv.Problems) > 0:
		return c, rv, &ProblemsError{rv.Problems}
	case normalizeCount(in.Confirm) != rv.ConfirmText:
		return c, rv, ErrConfirmMismatch
	case len(rv.ApprovalReasons) > 0 && s.approvals == nil:
		return c, rv, ErrNeedsApproval
	}

	now := s.now()
	scheduledAt := now
	if in.ScheduledAt != nil && in.ScheduledAt.After(now) {
		scheduledAt = *in.ScheduledAt
	}
	startAt := scheduledAt
	if earliest := now.Add(CancelWindow); startAt.Before(earliest) {
		startAt = earliest
	}
	htmlBody := withPreheader(tpl.HTML, c.Preheader)

	var out store.Campaign
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		locked, err := tx.LockCampaign(ctx, c.ID)
		if err != nil {
			return err
		}
		if !locked.State.Editable() {
			return ErrNotEditable
		}
		if locked.Version != in.Version {
			return store.ErrStale
		}
		capped, err := s.capped(ctx, tx, b.ID, locked)
		if err != nil {
			return err
		}
		included, excluded, err := tx.SnapshotAudience(ctx, locked, rv.TestSlicePct, capped)
		if err != nil {
			return err
		}
		// The audience can change between review and confirm (a signup,
		// an unsubscribe). The sender confirmed a number; hold them to it.
		if fmt.Sprint(included) != rv.ConfirmText {
			return ErrConfirmMismatch
		}
		reasons := make([]string, len(rv.ApprovalReasons))
		for i, r := range rv.ApprovalReasons {
			reasons[i] = string(r)
		}
		sc := store.CampaignSchedule{
			HTML: htmlBody, Text: withPreheaderText(tpl.TextBody, c.Preheader), FromHeader: rv.FromHeader,
			TestSlicePct: rv.TestSlicePct, HoldMinutes: rv.HoldMinutes, ScheduledAt: scheduledAt, StartAt: startAt,
			ConfirmedBy: by.ID, ApprovalReasons: reasons, AudienceCount: included, ExcludedCount: excluded,
			State: store.CampaignScheduled,
		}
		if sender, err := tx.BrandSender(ctx, b.ID); err != nil {
			return err
		} else {
			sc.ReplyTo = sender.ReplyTo
		}
		noSecondAdmin := false
		if len(rv.ApprovalReasons) > 0 {
			a, err := s.approvals.Request(ctx, tx, approvals.Request{
				Kind: kindFor(rv.ApprovalReasons), BrandID: &b.ID, RequestedBy: by,
				Summary: approvalSummary(locked, included, rv.ApprovalReasons),
				Payload: approvalPayload{CampaignID: locked.ID, Version: locked.Version, Audience: included, Reasons: rv.ApprovalReasons},
				Audit:   audit,
			})
			switch {
			case errors.Is(err, approvals.ErrNoApprover):
				// A company that has only ever had one admin can't get a
				// second opinion; it may send, on the record. Once it has
				// had two, a send waits until someone can decide.
				ok, err := s.approvals.ShortcutAllowed(ctx, tx)
				if err != nil {
					return err
				}
				if !ok {
					return ErrNeedsApproval
				}
				noSecondAdmin = true
			case err != nil:
				return err
			default:
				sc.State, sc.ApprovalID = store.CampaignNeedsApproval, &a.ID
			}
		}
		if out, err = tx.ScheduleCampaign(ctx, c.ID, sc); err != nil {
			return err
		}
		audit.Action, audit.TargetType, audit.TargetID = "campaign.scheduled", "campaign", c.ID.String()
		return tx.Audit(ctx, audit, map[string]any{"state": locked.State}, map[string]any{
			"state": out.State, "audience": included, "excluded": excluded, "start_at": startAt,
			"template_id": tpl.ID, "subject": c.Subject, "approval_reasons": reasons, "no_second_admin": noSecondAdmin,
		})
	})
	if err != nil {
		return c, rv, err
	}
	if out.State == store.CampaignScheduled {
		s.queue(ctx, out.ID, startAt)
	}
	return out, rv, nil
}

// normalizeCount accepts "2,431", "2 431", "2.431" or "2431"; anything else
// (letters, a minus sign) gives "".
func normalizeCount(s string) string {
	var digits strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r == ',' || r == '.' || r == ' ' || r == ' ' || r == ' ':
		default:
			return ""
		}
	}
	if digits.Len() == 0 {
		return ""
	}
	if n := strings.TrimLeft(digits.String(), "0"); n != "" {
		return n
	}
	return "0"
}

// withPreheader puts the campaign's preview text first in the body, hidden,
// so inboxes show it next to the subject.
func withPreheader(htmlBody, preheader string) string {
	if strings.TrimSpace(preheader) == "" {
		return htmlBody
	}
	div := `<div style="display:none;font-size:1px;line-height:1px;max-height:0;max-width:0;opacity:0;overflow:hidden;mso-hide:all">` +
		html.EscapeString(preheader) + `</div>`
	lower := strings.ToLower(htmlBody)
	i := strings.Index(lower, "<body")
	if i < 0 {
		return div + htmlBody
	}
	j := strings.Index(lower[i:], ">")
	if j < 0 {
		return div + htmlBody
	}
	at := i + j + 1
	return htmlBody[:at] + div + htmlBody[at:]
}

func withPreheaderText(text, preheader string) string {
	if strings.TrimSpace(preheader) == "" {
		return text
	}
	return preheader + "\n\n" + text
}

// Cancel stops a campaign that hasn't finished. Queued sends are skipped.
func (s *Service) Cancel(ctx context.Context, c store.Campaign, audit store.AuditEntry) (store.Campaign, error) {
	var out store.Campaign
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		ok, err := tx.SetCampaignState(ctx, c.ID, []store.CampaignState{
			store.CampaignDraft, store.CampaignNeedsApproval, store.CampaignScheduled, store.CampaignTestSlice,
			store.CampaignHolding, store.CampaignSending, store.CampaignPaused,
		}, store.CampaignCancelled)
		if err != nil {
			return err
		}
		if !ok {
			return ErrFinished
		}
		held, err := tx.SkipQueuedSends(ctx, c.ID, "cancelled")
		if err != nil {
			return err
		}
		if err := s.giveBack(ctx, tx, c.BrandID, held); err != nil {
			return err
		}
		audit.Action, audit.TargetType, audit.TargetID = "campaign.cancelled", "campaign", c.ID.String()
		if err := tx.Audit(ctx, audit, map[string]any{"state": c.State}, map[string]any{"state": store.CampaignCancelled}); err != nil {
			return err
		}
		out, err = tx.CampaignByID(ctx, c.BrandID, c.ID)
		return err
	})
	return out, err
}

var ErrFinished = errors.New("campaign already finished")
var ErrNotRunning = errors.New("campaign isn't sending")

// Pause holds a running campaign; Resume continues it.
func (s *Service) Pause(ctx context.Context, c store.Campaign, reason string, audit store.AuditEntry) (store.Campaign, error) {
	var out store.Campaign
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		ok, err := tx.PauseCampaign(ctx, c.ID, reason)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotRunning
		}
		audit.Action, audit.TargetType, audit.TargetID = "campaign.paused", "campaign", c.ID.String()
		if err := tx.Audit(ctx, audit, map[string]any{"state": c.State}, map[string]any{"state": "paused", "reason": reason}); err != nil {
			return err
		}
		out, err = tx.CampaignByID(ctx, c.BrandID, c.ID)
		return err
	})
	return out, err
}

func (s *Service) Resume(ctx context.Context, c store.Campaign, audit store.AuditEntry) (store.Campaign, error) {
	var out store.Campaign
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		to, ok, err := tx.ResumeCampaign(ctx, c.ID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotRunning
		}
		audit.Action, audit.TargetType, audit.TargetID = "campaign.resumed", "campaign", c.ID.String()
		if err := tx.Audit(ctx, audit, map[string]any{"state": "paused"}, map[string]any{"state": to}); err != nil {
			return err
		}
		out, err = tx.CampaignByID(ctx, c.BrandID, c.ID)
		return err
	})
	if err == nil {
		s.queue(ctx, c.ID, s.now())
	}
	return out, err
}

// --- the worker ---

// StepTimeout bounds one worker step (the river job's timeout is longer).
// Batches are sized to finish well inside it at the configured send rate.
const StepTimeout = 2 * time.Minute

// stuckAfter: a send left in 'sending' longer than this belongs to a worker
// that died (it is longer than any step can run).
const stuckAfter = StepTimeout + 3*time.Minute

// batchSize is how many sends one step claims: at most Batch, and no more
// than a minute of sending at the configured rate.
func (s *Service) batchSize() int {
	n := int(s.rate * 60)
	return max(1, min(Batch, n))
}

// step collects when the campaign's next step should run. It is queued
// only after this step has given up its lease, so the next one can take it.
type step struct {
	next *time.Time
}

func (st *step) then(at time.Time) {
	if st.next == nil || at.Before(*st.next) {
		st.next = &at
	}
}

// Step does whatever the campaign needs next. One worker at a time holds a
// campaign's lease; any other step for it returns at once. Every transition
// also checks the state it starts from, and sends are claimed with row
// locks, so even a lease that expired early can't send anything twice.
func (s *Service) Step(ctx context.Context, campaignID uuid.UUID) error {
	lease, ok, err := s.store.AcquireStepLease(ctx, campaignID, StepTimeout+time.Minute)
	if err != nil || !ok {
		return err // someone else is on it
	}
	var st step
	err = func() error {
		ctx, cancel := context.WithTimeout(ctx, StepTimeout)
		defer cancel()
		c, err := s.store.CampaignForStep(ctx, campaignID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		switch c.State {
		case store.CampaignScheduled:
			return s.start(ctx, c, &st)
		case store.CampaignTestSlice:
			return s.batch(ctx, c, "test", &st)
		case store.CampaignHolding:
			return s.holdEnd(ctx, c, &st)
		case store.CampaignSending:
			return s.batch(ctx, c, "rest", &st)
		}
		return nil // draft, needs approval, paused, done, cancelled: nothing to do
	}()
	bg := context.WithoutCancel(ctx)
	held, rerr := s.store.ReleaseStepLease(bg, campaignID, lease)
	if rerr != nil && err == nil {
		err = rerr
	}
	// A step that lost its lease (it stalled, and another step took over)
	// leaves what's next to that one: one chain per campaign.
	if st.next != nil && held {
		s.queue(bg, campaignID, *st.next)
	}
	return err
}

// Sweep restarts campaigns whose chain of steps was lost (a crash, a failed
// queue). One with a step already waiting or running is left alone, so
// there is never more than one chain per campaign.
func (s *Service) Sweep(ctx context.Context) error {
	var errs []error
	if err := s.cleanLeftovers(ctx); err != nil {
		errs = append(errs, err)
	}
	ids, err := s.store.CampaignsWithWork(ctx, stuckAfter)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, id := range ids {
		queued, err := s.store.StepQueued(ctx, id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if queued && s.runner != nil {
			continue
		}
		if err := s.Step(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("campaign %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// cleanLeftovers ends sends still waiting in cancelled or finished
// campaigns (a worker that crashed after a cancel) and gives back their
// warm-up and frequency-cap room, so they don't count against anyone.
func (s *Service) cleanLeftovers(ctx context.Context) error {
	left, err := s.store.CampaignLeftovers(ctx, stuckAfter)
	if err != nil {
		return err
	}
	var errs []error
	for _, l := range left {
		reason := "cancelled"
		if l.State != store.CampaignCancelled {
			reason = "campaign_ended"
		}
		err := s.store.InTx(ctx, func(tx *store.Store) error {
			res, err := tx.SkipLeftovers(ctx, l.CampaignID, stuckAfter, reason)
			if err != nil {
				return err
			}
			return s.giveBack(ctx, tx, l.BrandID, res)
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("campaign %s leftovers: %w", l.CampaignID, err))
		}
	}
	return errors.Join(errs...)
}

// putBack returns claimed sends (one claim) to the queue with requeue —
// unless the campaign was cancelled or finished meanwhile: then they are
// skipped and give back their reservations. The campaign row is share-locked
// for the transaction, so a concurrent Cancel either runs after this (and
// skips what it put back) or before (and this sees it).
func (s *Service) putBack(ctx context.Context, c store.Campaign, token uuid.UUID, ids []int64, requeue func(tx *store.Store) error) error {
	if len(ids) == 0 {
		return nil
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		state, err := tx.CampaignStateShared(ctx, c.ID)
		if err != nil {
			return err
		}
		if state == store.CampaignCancelled || state == store.CampaignDone {
			res, err := tx.SkipClaimedSends(ctx, token, ids, "cancelled")
			if err != nil {
				return err
			}
			return s.giveBack(ctx, tx, c.BrandID, res)
		}
		return requeue(tx)
	})
}

// release puts claimed sends back untouched (see putBack).
func (s *Service) release(ctx context.Context, c store.Campaign, token uuid.UUID, ids []int64) error {
	return s.putBack(ctx, c, token, ids, func(tx *store.Store) error { return tx.ReleaseClaimedSends(ctx, token, ids) })
}

// brandCanSend reports whether the brand may send right now: ready (DNS
// and SES verified) and not paused. The daily DNS check can move a ready
// brand back to setup at any time.
func brandCanSend(b store.Brand) bool { return b.Status == store.BrandReady }

// WaitingBrandNotReady marks a campaign paused because its brand stopped
// being able to send.
const WaitingBrandNotReady = "brand_not_ready"

// pauseForBrand pauses a running campaign because the brand can't send.
func (s *Service) pauseForBrand(ctx context.Context, c store.Campaign, b store.Brand) error {
	why := "the brand's DNS or SES setup stopped checking out; fix it on the brand's page, then resume"
	if b.Status == store.BrandPaused {
		why = "the brand was paused"
	}
	if _, err := s.store.PauseCampaign(ctx, c.ID, why); err != nil {
		return err
	}
	s.log.WarnContext(ctx, "campaign paused: brand can't send", "campaign_id", c.ID, "brand_status", b.Status)
	return s.store.SetCampaignWaiting(ctx, c.ID, WaitingBrandNotReady)
}

func (s *Service) start(ctx context.Context, c store.Campaign, st *step) error {
	now := s.now()
	if c.StartAt != nil && now.Before(*c.StartAt) {
		st.then(*c.StartAt)
		return nil
	}
	b, err := s.store.BrandByID(ctx, c.BrandID)
	if err != nil {
		return err
	}
	// The test stage is queued whatever happens next, so a campaign that
	// starts paused still begins with its test slice when resumed.
	if err := s.queueStage(ctx, c, "test"); err != nil {
		return err
	}
	ok, err := s.store.SetCampaignState(ctx, c.ID, []store.CampaignState{store.CampaignScheduled}, store.CampaignTestSlice)
	if err != nil || !ok {
		return err
	}
	if !brandCanSend(b) {
		return s.pauseForBrand(ctx, c, b)
	}
	s.log.InfoContext(ctx, "campaign started", "campaign_id", c.ID, "brand", b.Slug, "audience", c.AudienceCount)
	st.then(s.now())
	return nil
}

// queueStage creates the stage's missing send rows, each due when the
// recipient is outside quiet hours. Safe to repeat.
func (s *Service) queueStage(ctx context.Context, c store.Campaign, stage string) error {
	recips, err := s.store.StageRecipients(ctx, c.ID, stage)
	if err != nil || len(recips) == 0 {
		return err
	}
	now := s.now()
	ids := make([]uuid.UUID, len(recips))
	after := make([]time.Time, len(recips))
	for i, r := range recips {
		ids[i], after[i] = r.ContactID, sending.SendAfter(now, r.Timezone)
	}
	return s.store.QueueSends(ctx, c, stage, ids, after)
}

func (s *Service) holdEnd(ctx context.Context, c store.Campaign, st *step) error {
	now := s.now()
	if c.HoldUntil != nil && now.Before(*c.HoldUntil) {
		st.then(*c.HoldUntil)
		return nil
	}
	b, err := s.store.BrandByID(ctx, c.BrandID)
	if err != nil {
		return err
	}
	if !brandCanSend(b) {
		return s.pauseForBrand(ctx, c, b)
	}
	if s.brakes != nil {
		pause, reason, err := s.brakes.AfterTestSlice(ctx, c)
		if err != nil {
			return err
		}
		if pause {
			_, err := s.store.PauseCampaign(ctx, c.ID, reason)
			return err
		}
	}
	if err := s.queueStage(ctx, c, "rest"); err != nil {
		return err
	}
	ok, err := s.store.SetCampaignState(ctx, c.ID, []store.CampaignState{store.CampaignHolding}, store.CampaignSending)
	if err != nil || !ok {
		return err
	}
	st.then(s.now())
	return nil
}

// batch sends one batch of due emails of a stage, then decides what's next.
func (s *Service) batch(ctx context.Context, c store.Campaign, stage string, st *step) error {
	// Bookkeeping must happen even when the step's time is up.
	bg := context.WithoutCancel(ctx)
	if _, err := s.store.MarkStuckSends(ctx, c.ID, stuckAfter); err != nil {
		return err
	}
	b, err := s.store.BrandByID(ctx, c.BrandID)
	if err != nil {
		return err
	}
	if !brandCanSend(b) {
		return s.pauseForBrand(ctx, c, b)
	}
	claimed, err := s.claim(ctx, c, stage)
	if err != nil {
		return err
	}
	base := unsubscribe.BaseURL(b.LinkDomain, s.baseURL)
	release := func(rest []store.ClaimedSend) error {
		if len(rest) == 0 {
			return nil
		}
		ids := make([]int64, len(rest))
		for i, r := range rest {
			ids[i] = r.ID
		}
		return s.release(bg, c, rest[0].Token, ids)
	}
	for i, cs := range claimed {
		// Stop claiming time we don't have: put the rest back for the next step.
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) < 10*time.Second {
			st.then(s.now())
			return release(claimed[i:])
		}
		stop, err := s.sendOne(ctx, b, c, cs, base)
		if err != nil || stop {
			if rerr := release(claimed[i+1:]); rerr != nil && err == nil {
				err = rerr
			}
			if ctx.Err() != nil {
				st.then(s.now()) // out of time, not stopped: carry on in the next step
			}
			return err
		}
	}
	return s.afterBatch(bg, c, stage, len(claimed), st)
}

// claim takes the next batch of due sends and reserves their room in the
// brand's warm-up allowance and the frequency cap, all in one transaction
// (and only through it), so concurrent workers can't overspend either.
// Whatever the warm-up doesn't grant waits for the recipient's next morning
// after the UTC day turns; whoever the cap holds back is skipped. A send
// that comes back to the queue (a retry, a pause) keeps its reservation
// until it is claimed again, and gives it back then; one that ends without
// going out gives it back at once.
func (s *Service) claim(ctx context.Context, c store.Campaign, stage string) ([]store.ClaimedSend, error) {
	var claimed []store.ClaimedSend
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		// The campaign first (lock order: campaign, then sends), and only
		// while it is still in this stage: a cancel or pause that committed
		// first wins.
		state, err := tx.CampaignStateForClaim(ctx, c.ID)
		if err != nil || state != c.State {
			return err
		}
		if claimed, err = tx.ClaimSends(ctx, c, stage, s.batchSize()); err != nil || len(claimed) == 0 {
			return err
		}
		// Microseconds, as Postgres keeps them: Release matches on it.
		now := s.now().Truncate(time.Microsecond)
		var old []store.Reservation
		var oldIDs []int64
		for i, cs := range claimed {
			if cs.ReservedAt != nil {
				old = append(old, store.Reservation{ContactID: cs.ContactID, ReservedAt: *cs.ReservedAt})
				oldIDs = append(oldIDs, cs.ID)
				claimed[i].ReservedAt = nil
			}
		}
		if len(old) > 0 {
			if err := s.giveBack(ctx, tx, c.BrandID, old); err != nil {
				return err
			}
			if err := tx.SetReserved(ctx, oldIDs, nil); err != nil {
				return err
			}
		}
		if s.warmup != nil {
			granted, err := s.warmup.Reserve(ctx, tx, c.BrandID, now, len(claimed))
			if err != nil {
				return err
			}
			waiting := ""
			if granted < len(claimed) {
				tomorrow := now.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
				ids := make([]int64, 0, len(claimed)-granted)
				after := make([]time.Time, 0, len(claimed)-granted)
				for _, cs := range claimed[granted:] {
					ids = append(ids, cs.ID)
					after = append(after, sending.SendAfter(tomorrow, cs.Timezone))
				}
				if err := tx.DeferSends(ctx, claimed[0].Token, ids, after); err != nil {
					return err
				}
				claimed = claimed[:granted]
				waiting = "the brand's warm-up allowance for today is used up; sending continues tomorrow"
			}
			if err := tx.SetCampaignWaiting(ctx, c.ID, waiting); err != nil {
				return err
			}
		}
		// The frequency cap runs on what the warm-up granted. Someone over
		// it is skipped for this campaign, not deferred: a later send would
		// be just as unwelcome. Their warm-up room goes back.
		if s.freqcap != nil && len(claimed) > 0 {
			ids := make([]uuid.UUID, len(claimed))
			for i, cs := range claimed {
				ids[i] = cs.ContactID
			}
			_, excluded, err := s.freqcap.Reserve(ctx, tx, c.BrandID, ids, now)
			if err != nil {
				return err
			}
			reasons := make(map[uuid.UUID]freqcap.Reason, len(excluded))
			for _, e := range excluded {
				reasons[e.ContactID] = e.Reason
			}
			kept := claimed[:0]
			for _, cs := range claimed {
				if reason, out := reasons[cs.ContactID]; out {
					if _, err := tx.MarkSendSkipped(ctx, cs.Claim(), string(reason)); err != nil {
						return err
					}
					continue
				}
				kept = append(kept, cs)
			}
			if s.warmup != nil {
				if err := s.warmup.Release(ctx, tx, c.BrandID, now, len(claimed)-len(kept)); err != nil {
					return err
				}
			}
			claimed = kept
		}
		if (s.warmup == nil && s.freqcap == nil) || len(claimed) == 0 {
			return nil
		}
		ids := make([]int64, len(claimed))
		for i := range claimed {
			ids[i] = claimed[i].ID
			claimed[i].ReservedAt = &now
		}
		return tx.SetReserved(ctx, ids, &now)
	})
	return claimed, err
}

// giveBack returns reservations of sends that didn't go out, through tx.
func (s *Service) giveBack(ctx context.Context, tx *store.Store, brandID uuid.UUID, res []store.Reservation) error {
	byTime := map[time.Time][]uuid.UUID{}
	for _, r := range res {
		at := r.ReservedAt.UTC()
		byTime[at] = append(byTime[at], r.ContactID)
	}
	for at, ids := range byTime {
		if s.warmup != nil {
			if err := s.warmup.Release(ctx, tx, brandID, at, len(ids)); err != nil {
				return err
			}
		}
		if s.freqcap != nil {
			if err := s.freqcap.Release(ctx, tx, brandID, ids, at); err != nil {
				return err
			}
		}
	}
	return nil
}

// endUnsent ends a send that didn't go out (skipped, failed) and gives its
// reservation back, in one transaction.
func (s *Service) endUnsent(ctx context.Context, c store.Campaign, cs store.ClaimedSend, mark func(*store.Store) (*time.Time, error)) error {
	return s.store.InTx(ctx, func(tx *store.Store) error {
		at, err := mark(tx)
		if err != nil || at == nil {
			return err
		}
		return s.giveBack(ctx, tx, c.BrandID, []store.Reservation{{ContactID: cs.ContactID, ReservedAt: *at}})
	})
}

func (s *Service) skip(ctx context.Context, c store.Campaign, cs store.ClaimedSend, reason string) error {
	return s.endUnsent(ctx, c, cs, func(tx *store.Store) (*time.Time, error) { return tx.MarkSendSkipped(ctx, cs.Claim(), reason) })
}

// audience counts a draft's audience as it would be now, the frequency cap
// included (S5). Read through q.
func (s *Service) audience(ctx context.Context, q *store.Store, brandID uuid.UUID, c store.Campaign) (store.AudienceSummary, error) {
	capped, err := s.capped(ctx, q, brandID, c)
	if err != nil {
		return store.AudienceSummary{}, err
	}
	return q.AudienceSummary(ctx, brandID, c.TopicID, c.IncludeAtRisk, capped)
}

// capped lists who the frequency cap would hold back from the campaign now.
// Sending checks again, so this is the forecast the sender confirms.
func (s *Service) capped(ctx context.Context, q *store.Store, brandID uuid.UUID, c store.Campaign) ([]uuid.UUID, error) {
	if s.freqcap == nil {
		return nil, nil
	}
	ids, err := q.AudienceCandidates(ctx, brandID, c.TopicID, c.IncludeAtRisk)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	_, excluded, err := s.freqcap.Filter(ctx, q, brandID, ids, s.now())
	if err != nil {
		return nil, err
	}
	out := make([]uuid.UUID, len(excluded))
	for i, e := range excluded {
		out[i] = e.ContactID
	}
	return out, nil
}

// sendOne re-checks one recipient, sends, and records the outcome. stop
// means this step must stop (paused, cancelled, brand or SES can't send).
func (s *Service) sendOne(ctx context.Context, b store.Brand, c store.Campaign, cs store.ClaimedSend, base string) (stop bool, err error) {
	bg := context.WithoutCancel(ctx)
	// Re-check right before sending: people unsubscribe, bounce or get
	// erased while a campaign is under way.
	switch {
	case cs.Email == nil:
		return false, s.skip(bg, c, cs, "erased")
	case cs.Suppressed:
		return false, s.skip(bg, c, cs, "suppressed")
	case cs.Status == nil || *cs.Status != string(store.ContactSubscribed):
		return false, s.skip(bg, c, cs, "no_longer_subscribed")
	case !cs.InTopic:
		return false, s.skip(bg, c, cs, "topic_unsubscribed")
	}
	msg := s.message(b, c, cs, base)
	if err := s.pacer.Wait(ctx); err != nil {
		return true, s.release(bg, c, cs.Token, []int64{cs.ID})
	}
	// Re-read campaign and brand after the wait, right before sending, so a
	// pause, a cancel or a failing DNS check stops it within one email.
	state, err := s.store.CampaignState(ctx, c.ID)
	if err != nil || state != c.State {
		return true, errors.Join(err, s.release(bg, c, cs.Token, []int64{cs.ID}))
	}
	now, err := s.store.BrandByID(ctx, b.ID)
	if err != nil {
		return true, errors.Join(err, s.release(bg, c, cs.Token, []int64{cs.ID}))
	}
	if !brandCanSend(now) {
		return true, errors.Join(s.release(bg, c, cs.Token, []int64{cs.ID}), s.pauseForBrand(bg, c, now))
	}

	mine, err := s.store.MarkCalling(bg, cs.Claim())
	if err != nil {
		return true, errors.Join(err, s.release(bg, c, cs.Token, []int64{cs.ID}))
	}
	if !mine {
		// This worker stalled long enough for its claim to be recovered and
		// handed to another: that one sends it. Our lease is gone too.
		s.log.WarnContext(ctx, "claim lost; not sending", "campaign_id", c.ID, "send_id", cs.ID)
		return true, nil
	}
	id, err := s.sender.Send(ctx, msg)
	switch {
	case err == nil:
		return false, s.store.MarkSent(bg, cs.Claim(), id)
	case errors.Is(err, ses.ErrThrottled):
		s.pacer.Slow(time.Second)
		// Not sent, and not the recipient's fault: don't count the attempt.
		after := sending.SendAfter(s.now().Add(2*time.Second), cs.Timezone)
		return false, s.putBack(bg, c, cs.Token, []int64{cs.ID}, func(tx *store.Store) error {
			return tx.RequeueSendUncounted(bg, cs.Claim(), after, err.Error())
		})
	case errors.Is(err, ses.ErrUnavailable) && cs.Attempts < MaxAttempts:
		backoff := time.Duration(math.Pow(4, float64(cs.Attempts))) * 15 * time.Second
		after := sending.SendAfter(s.now().Add(backoff), cs.Timezone)
		return false, s.putBack(bg, c, cs.Token, []int64{cs.ID}, func(tx *store.Store) error {
			return tx.RequeueSend(bg, cs.Claim(), after, err.Error())
		})
	case errors.Is(err, ses.ErrUnavailable), errors.Is(err, ses.ErrRejected):
		return false, s.endUnsent(bg, c, cs, func(tx *store.Store) (*time.Time, error) { return tx.MarkSendFailed(bg, cs.Claim(), err.Error()) })
	case errors.Is(err, ses.ErrSendingPaused):
		if rerr := s.release(bg, c, cs.Token, []int64{cs.ID}); rerr != nil {
			return true, rerr
		}
		_, perr := s.store.PauseCampaign(bg, c.ID, "Amazon SES stopped sending: "+err.Error())
		return true, perr
	default:
		// A timeout, a dropped connection, an unexpected error: SES may have
		// accepted it. Never retried, so nobody gets it twice.
		s.log.WarnContext(ctx, "send outcome unknown", "campaign_id", c.ID, "send_id", cs.ID, "err", err)
		return ctx.Err() != nil, s.store.MarkSendUnknown(bg, cs.Claim(), "outcome unknown, not retried: "+err.Error())
	}
}

// message renders one email for one person.
func (s *Service) message(b store.Brand, c store.Campaign, cs store.ClaimedSend, base string) ses.Message {
	claims := unsubscribe.Claims{BrandID: b.ID, ContactID: cs.ContactID, CampaignID: c.ID, TopicID: c.TopicID}
	vars := map[string]string{
		"first_name": oneLine(cs.FirstName), "last_name": oneLine(cs.LastName), "email": *cs.Email,
		"brand_name": b.Name, "postal_address": b.PostalAddress,
		"unsubscribe_url": s.signer.PageURL(base, claims), "preferences_url": s.signer.PreferencesURL(base, claims),
	}
	lu, lup := unsubscribe.Headers(s.signer.OneClickURL(base, claims))
	return ses.Message{
		From: c.FromHeader, ReplyTo: c.ReplyTo, To: *cs.Email,
		Subject: oneLine(templates.RenderText(c.Subject, vars)),
		HTML:    templates.Render(c.HTML, vars), Text: templates.RenderText(c.TextBody, vars),
		Headers:          map[string]string{"List-Unsubscribe": lu, "List-Unsubscribe-Post": lup},
		ConfigurationSet: b.SESConfigurationSet,
		Tags:             map[string]string{"campaign_id": c.ID.String(), "send_id": fmt.Sprint(cs.ID)},
	}
}

// oneLine keeps a value out of other headers: no line breaks.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.NewReplacer("\r", " ", "\n", " ").Replace(s)), " ")
}

// afterBatch schedules the next batch, or moves the campaign on when the
// stage has nothing left.
func (s *Service) afterBatch(ctx context.Context, c store.Campaign, stage string, sent int, st *step) error {
	next, err := s.store.NextSendAt(ctx, c.ID, stage)
	if err != nil {
		return err
	}
	now := s.now()
	switch {
	case next != nil && !next.After(now) && sent > 0:
		st.then(now)
		return nil
	case next != nil:
		// Waiting for quiet hours to end or a retry to come due.
		at := *next
		if !at.After(now) {
			at = now.Add(time.Second) // claimed by another worker; check back
		}
		st.then(at)
		return nil
	}
	if stage == "test" {
		// Never watch a test slice that wasn't sent: if the test stage has
		// no sends yet (it was never queued), queue it and send it first.
		n, err := s.store.CountStageSends(ctx, c.ID, "test")
		if err != nil {
			return err
		}
		if n == 0 {
			if err := s.queueStage(ctx, c, "test"); err != nil {
				return err
			}
			if n, err = s.store.CountStageSends(ctx, c.ID, "test"); err != nil {
				return err
			}
			if n > 0 {
				st.then(s.now())
				return nil
			}
		}
		until := now.Add(time.Duration(*c.HoldMinutes) * time.Minute)
		if err := s.store.SetCampaignHold(ctx, c.ID, until); err != nil {
			return err
		}
		ok, err := s.store.SetCampaignState(ctx, c.ID, []store.CampaignState{store.CampaignTestSlice}, store.CampaignHolding)
		if err != nil || !ok {
			return err
		}
		s.log.InfoContext(ctx, "campaign test slice sent; holding", "campaign_id", c.ID, "until", until)
		st.then(until)
		return nil
	}
	ok, err := s.store.SetCampaignState(ctx, c.ID, []store.CampaignState{store.CampaignSending}, store.CampaignDone)
	if err == nil && ok {
		s.log.InfoContext(ctx, "campaign done", "campaign_id", c.ID)
	}
	return err
}

// --- test sends ---

// MaxTestRecipients per test send.
const MaxTestRecipients = 10

// SendTest sends the campaign's current template to seed addresses, marked
// as a test. The links in it work but don't unsubscribe anyone.
func (s *Service) SendTest(ctx context.Context, b store.Brand, c store.Campaign, to []string) (int, error) {
	if c.TemplateID == nil {
		return 0, &ProblemsError{[]string{"choose a template first"}}
	}
	tpl, err := s.store.TemplateByID(ctx, b.ID, *c.TemplateID)
	if err != nil {
		return 0, err
	}
	switch {
	case tpl.ErrorCount > 0:
		return 0, &ProblemsError{[]string{fmt.Sprintf("the template has %d error(s) to fix first", tpl.ErrorCount)}}
	case tpl.Stale:
		return 0, &ProblemsError{[]string{"the template is being re-checked after a layout change; try again in a minute"}}
	}
	sender, err := s.store.BrandSender(ctx, b.ID)
	if err != nil {
		return 0, err
	}
	base := unsubscribe.BaseURL(b.LinkDomain, s.baseURL)
	// No contact: the links lead to a page that explains it was a test.
	claims := unsubscribe.Claims{BrandID: b.ID, CampaignID: c.ID, TopicID: c.TopicID}
	vars := templates.SampleVars(b.Name, b.PostalAddress)
	vars["unsubscribe_url"], vars["preferences_url"] = s.signer.PageURL(base, claims), s.signer.PreferencesURL(base, claims)
	lu, lup := unsubscribe.Headers(s.signer.OneClickURL(base, claims))
	sent := 0
	for _, addr := range to {
		vars["email"] = addr
		m := ses.Message{
			From: FromHeader(b, sender), ReplyTo: sender.ReplyTo, To: addr,
			Subject:          "[Test] " + oneLine(templates.RenderText(c.Subject, vars)),
			HTML:             templates.Render(withPreheader(tpl.HTML, c.Preheader), vars),
			Text:             templates.RenderText(withPreheaderText(tpl.TextBody, c.Preheader), vars),
			Headers:          map[string]string{"List-Unsubscribe": lu, "List-Unsubscribe-Post": lup},
			ConfigurationSet: b.SESConfigurationSet,
			Tags:             map[string]string{"campaign_id": c.ID.String(), "test": "true"},
		}
		if err := s.pacer.Wait(ctx); err != nil {
			return sent, err
		}
		if _, err := s.sender.Send(ctx, m); err != nil {
			return sent, fmt.Errorf("sending the test to %s: %w", addr, err)
		}
		sent++
	}
	return sent, nil
}

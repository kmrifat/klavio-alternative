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
	// InWarmup reports whether sends on day need a warm-up approval.
	InWarmup(ctx context.Context, brandID uuid.UUID, day time.Time) (bool, error)
}

// Brakes decides after the test slice whether the rest may go (M4 reads
// bounces and complaints). Until then every campaign continues.
type Brakes interface {
	AfterTestSlice(ctx context.Context, c store.Campaign) (pause bool, reason string, err error)
}

// FreqCap keeps one person from getting too much email across brands (M7).
// Until it is wired in, there is no cap.
type FreqCap interface {
	// Reserve checks and records sends for these contacts inside tx and
	// returns who may get one now; the rest map to a reason
	// ("freq_cap_day", "freq_cap_week", "same_brand_gap" …).
	Reserve(ctx context.Context, tx *store.Store, brandID uuid.UUID, contactIDs []uuid.UUID, now time.Time) (allowed []uuid.UUID, excluded map[uuid.UUID]string, err error)
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
		limits: DefaultLimits{}, pacer: sending.NewPacer(cfg.SendsPerSecond), now: time.Now,
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
	sum, err := s.store.AudienceSummary(ctx, b.ID, c.TopicID, c.IncludeAtRisk)
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
		included, excluded, err := tx.SnapshotAudience(ctx, locked, rv.TestSlicePct)
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
		if err := tx.SkipQueuedSends(ctx, c.ID, "cancelled"); err != nil {
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

// Step does whatever the campaign needs next. It's safe to call any number
// of times, from any number of workers: every transition checks the state
// it starts from, and sends are claimed with row locks.
func (s *Service) Step(ctx context.Context, campaignID uuid.UUID) error {
	c, err := s.store.CampaignForStep(ctx, campaignID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	switch c.State {
	case store.CampaignScheduled:
		return s.start(ctx, c)
	case store.CampaignTestSlice:
		return s.batch(ctx, c, "test")
	case store.CampaignHolding:
		return s.holdEnd(ctx, c)
	case store.CampaignSending:
		return s.batch(ctx, c, "rest")
	}
	return nil // draft, needs approval, paused, done, cancelled: nothing to do
}

// Sweep runs a step for every campaign that may have work, in case a queued
// step was lost. A periodic job calls it.
func (s *Service) Sweep(ctx context.Context) error {
	ids, err := s.store.CampaignsWithWork(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		if err := s.Step(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("campaign %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) start(ctx context.Context, c store.Campaign) error {
	now := s.now()
	if c.StartAt != nil && now.Before(*c.StartAt) {
		s.queue(ctx, c.ID, *c.StartAt)
		return nil
	}
	b, err := s.store.BrandByID(ctx, c.BrandID)
	if err != nil {
		return err
	}
	if b.Status != store.BrandReady {
		// Start anyway in a paused state, so it's visible and resumable.
		if _, err := s.store.SetCampaignState(ctx, c.ID, []store.CampaignState{store.CampaignScheduled}, store.CampaignTestSlice); err != nil {
			return err
		}
		_, err := s.store.PauseCampaign(ctx, c.ID, fmt.Sprintf("the brand is %s, so sending didn't start", b.Status))
		return err
	}
	if err := s.queueStage(ctx, c, "test"); err != nil {
		return err
	}
	ok, err := s.store.SetCampaignState(ctx, c.ID, []store.CampaignState{store.CampaignScheduled}, store.CampaignTestSlice)
	if err != nil || !ok {
		return err
	}
	s.log.InfoContext(ctx, "campaign started", "campaign_id", c.ID, "brand", b.Slug, "audience", c.AudienceCount)
	s.queue(ctx, c.ID, s.now())
	return nil
}

// queueStage creates the stage's send rows, each due when the recipient is
// outside quiet hours.
func (s *Service) queueStage(ctx context.Context, c store.Campaign, stage string) error {
	recips, err := s.store.StageRecipients(ctx, c.ID, stage)
	if err != nil {
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

func (s *Service) holdEnd(ctx context.Context, c store.Campaign) error {
	now := s.now()
	if c.HoldUntil != nil && now.Before(*c.HoldUntil) {
		s.queue(ctx, c.ID, *c.HoldUntil)
		return nil
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
	s.queue(ctx, c.ID, s.now())
	return nil
}

// batch sends up to Batch due emails of a stage, then decides what's next.
func (s *Service) batch(ctx context.Context, c store.Campaign, stage string) error {
	if _, err := s.store.MarkStuckSends(ctx, c.ID, 10*time.Minute); err != nil {
		return err
	}
	b, err := s.store.BrandByID(ctx, c.BrandID)
	if err != nil {
		return err
	}
	if b.Status == store.BrandPaused {
		_, err := s.store.PauseCampaign(ctx, c.ID, "the brand was paused")
		return err
	}
	claimed, err := s.claim(ctx, c, stage)
	if err != nil {
		return err
	}
	base := unsubscribe.BaseURL(b.LinkDomain, s.baseURL)
	for i, cs := range claimed {
		stop, err := s.sendOne(ctx, b, c, cs, base)
		if err != nil || stop {
			// Put back what this batch hasn't touched.
			rest := make([]int64, 0, len(claimed)-i-1)
			for _, r := range claimed[i+1:] {
				rest = append(rest, r.ID)
			}
			if rerr := s.store.ReleaseSends(ctx, rest); rerr != nil && err == nil {
				err = rerr
			}
			return err
		}
	}
	return s.afterBatch(ctx, c, stage, len(claimed))
}

// claim takes the next batch of due sends. While the brand warms up, the
// batch is reserved from its daily allowance in the same transaction, so
// concurrent workers can't overspend it; whatever isn't granted waits for
// the next UTC day. A reserved send that later fails for good still counts
// against the day: conservative, and the warm-up is about volume offered.
func (s *Service) claim(ctx context.Context, c store.Campaign, stage string) ([]store.ClaimedSend, error) {
	var claimed []store.ClaimedSend
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		var err error
		if claimed, err = tx.ClaimSends(ctx, c, stage, Batch); err != nil || len(claimed) == 0 {
			return err
		}
		now := s.now()
		if s.warmup != nil {
			granted, err := s.warmup.Reserve(ctx, tx, c.BrandID, now, len(claimed))
			if err != nil {
				return err
			}
			waiting := ""
			if granted < len(claimed) {
				later := make([]int64, 0, len(claimed)-granted)
				for _, cs := range claimed[granted:] {
					later = append(later, cs.ID)
				}
				tomorrow := now.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
				if err := tx.DeferSends(ctx, later, tomorrow); err != nil {
					return err
				}
				claimed = claimed[:granted]
				waiting = "the brand's warm-up allowance for today is used up; sending continues tomorrow (UTC)"
			}
			if err := tx.SetCampaignWaiting(ctx, c.ID, waiting); err != nil {
				return err
			}
		}
		// The frequency cap runs on what the warm-up granted. Someone over
		// it is skipped for this campaign, not deferred: a later send would
		// be just as unwelcome. (The warm-up counted them anyway, which is
		// conservative.)
		if s.freqcap == nil || len(claimed) == 0 {
			return nil
		}
		ids := make([]uuid.UUID, len(claimed))
		for i, cs := range claimed {
			ids[i] = cs.ContactID
		}
		_, excluded, err := s.freqcap.Reserve(ctx, tx, c.BrandID, ids, now)
		if err != nil {
			return err
		}
		kept := claimed[:0]
		for _, cs := range claimed {
			if reason, out := excluded[cs.ContactID]; out {
				if err := tx.MarkSendSkipped(ctx, cs.ID, reason); err != nil {
					return err
				}
				continue
			}
			kept = append(kept, cs)
		}
		claimed = kept
		return nil
	})
	return claimed, err
}

// sendOne re-checks one recipient, sends, and records the outcome. stop
// means the whole campaign must stop (SES can't send at all).
func (s *Service) sendOne(ctx context.Context, b store.Brand, c store.Campaign, cs store.ClaimedSend, base string) (stop bool, err error) {
	// Re-check right before sending: people unsubscribe, bounce or get
	// erased while a campaign is under way.
	switch {
	case cs.Email == nil:
		return false, s.store.MarkSendSkipped(ctx, cs.ID, "erased")
	case cs.Suppressed:
		return false, s.store.MarkSendSkipped(ctx, cs.ID, "suppressed")
	case cs.Status == nil || *cs.Status != string(store.ContactSubscribed):
		return false, s.store.MarkSendSkipped(ctx, cs.ID, "no_longer_subscribed")
	case !cs.InTopic:
		return false, s.store.MarkSendSkipped(ctx, cs.ID, "topic_unsubscribed")
	}
	// Re-read the state before every send, so pausing stops within one email.
	if st, err := s.store.CampaignState(ctx, c.ID); err != nil || st != c.State {
		if err == nil {
			err = s.store.ReleaseSends(ctx, []int64{cs.ID})
		}
		return true, err
	}

	msg := s.message(b, c, cs, base)
	if err := s.pacer.Wait(ctx); err != nil {
		return true, s.store.ReleaseSends(ctx, []int64{cs.ID})
	}
	id, err := s.sender.Send(ctx, msg)
	switch {
	case err == nil:
		return false, s.store.MarkSent(ctx, cs.ID, id)
	case errors.Is(err, ses.ErrThrottled):
		s.pacer.Slow(time.Second)
		// Throttling isn't the recipient's fault: don't count the attempt.
		return false, s.store.RequeueSendUncounted(ctx, cs.ID, s.now().Add(2*time.Second), err.Error())
	case errors.Is(err, ses.ErrRejected):
		return false, s.store.MarkSendFailed(ctx, cs.ID, err.Error())
	case errors.Is(err, ses.ErrSendingPaused):
		if rerr := s.store.ReleaseSends(ctx, []int64{cs.ID}); rerr != nil {
			return true, rerr
		}
		_, perr := s.store.PauseCampaign(ctx, c.ID, "Amazon SES stopped sending: "+err.Error())
		return true, perr
	case cs.Attempts >= MaxAttempts:
		return false, s.store.MarkSendFailed(ctx, cs.ID, err.Error())
	default:
		backoff := time.Duration(math.Pow(4, float64(cs.Attempts))) * 15 * time.Second
		return false, s.store.RequeueSend(ctx, cs.ID, s.now().Add(backoff), err.Error())
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

// afterBatch queues the next batch, or moves the campaign on when the
// stage has nothing left.
func (s *Service) afterBatch(ctx context.Context, c store.Campaign, stage string, sent int) error {
	next, err := s.store.NextSendAt(ctx, c.ID, stage)
	if err != nil {
		return err
	}
	now := s.now()
	switch {
	case next != nil && !next.After(now) && sent > 0:
		s.queue(ctx, c.ID, now)
		return nil
	case next != nil:
		// Waiting for quiet hours to end or a retry to come due.
		at := *next
		if !at.After(now) {
			at = now.Add(time.Second) // claimed by another worker; check back
		}
		s.queue(ctx, c.ID, at)
		return nil
	}
	if stage == "test" {
		until := now.Add(time.Duration(*c.HoldMinutes) * time.Minute)
		if err := s.store.SetCampaignHold(ctx, c.ID, until); err != nil {
			return err
		}
		ok, err := s.store.SetCampaignState(ctx, c.ID, []store.CampaignState{store.CampaignTestSlice}, store.CampaignHolding)
		if err != nil || !ok {
			return err
		}
		s.log.InfoContext(ctx, "campaign test slice sent; holding", "campaign_id", c.ID, "until", until)
		s.queue(ctx, c.ID, until)
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
	if tpl.ErrorCount > 0 {
		return 0, &ProblemsError{[]string{fmt.Sprintf("the template has %d error(s) to fix first", tpl.ErrorCount)}}
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

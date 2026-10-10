// Package limits holds the company-wide safety limits (S17). Making a limit
// stricter applies at once; making it looser needs a second admin; nothing
// goes past the hard limit, and locked limits can't be loosened at all.
package limits

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/approvals"
	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// Def describes one limit. Percent values are stored as percents (2 = 2%).
type Def struct {
	Key   string  `json:"key"`
	Group string  `json:"group"` // brakes | audience | imports
	Label string  `json:"label"`
	Unit  string  `json:"unit"` // % | people | hours | minutes | months | per day
	Value float64 `json:"default"`
	Hard  float64 `json:"hard_limit"`
	// LooserIsHigher: raising the number makes sending riskier (a pause
	// threshold). False: lowering it is riskier (a minimum gap).
	LooserIsHigher bool `json:"looser_is_higher"`
	// Locked: can only be tightened.
	Locked bool   `json:"locked"`
	Help   string `json:"help"`
}

// Keys used by other packages.
const (
	CampaignBouncePct        = "campaign_pause_bounce_pct"
	CampaignComplaintPct     = "campaign_pause_complaint_pct"
	CampaignUnsubPct         = "campaign_pause_unsubscribe_pct"
	BrandBouncePct7d         = "brand_pause_bounce_pct_7d"
	BrandGmailSpamPct        = "brand_pause_gmail_spam_pct"
	AccountComplaintPct      = "account_brake_complaint_pct"
	CapPerDay                = "cross_brand_cap_per_day"
	CapPerWeek               = "cross_brand_cap_per_week"
	SameBrandGapHours        = "same_brand_min_gap_hours"
	ApprovalAbove            = "approval_needed_above"
	TestSlicePct             = "test_slice_pct"
	TestSliceHoldMinutes     = "test_slice_hold_minutes"
	ImportInvalidPct         = "import_invalid_pct"
	ImportRoleDisposablePct  = "import_role_disposable_pct"
	ImportQuarantineMonths   = "import_quarantine_months"
	RepermissionEmailsPerDay = "repermission_emails_per_day"
)

// Defs mirror S17 (now = default, hard limit as designed).
var Defs = []Def{
	{CampaignBouncePct, "brakes", "Campaign pause · bounces", "%", 2, 3, true, false, "A campaign pauses when this share of delivered email bounces."},
	{CampaignComplaintPct, "brakes", "Campaign pause · complaints (min 2)", "%", 0.08, 0.08, true, true, "Spam complaints that pause a campaign. Locked: Gmail's line is 0.1–0.3%."},
	{CampaignUnsubPct, "brakes", "Campaign pause · unsubscribes", "%", 1, 2, true, false, "A sign the audience is wrong."},
	{BrandBouncePct7d, "brakes", "Brand pause · bounces (7 days)", "%", 3, 4, true, false, ""},
	{BrandGmailSpamPct, "brakes", "Brand pause · Gmail spam rate", "%", 0.2, 0.25, true, false, "From Google Postmaster Tools."},
	{AccountComplaintPct, "brakes", "Account brake · complaints (all brands)", "%", 0.08, 0.08, true, true, "Protects the shared SES account."},
	{CapPerDay, "audience", "Cross-brand cap · per person per day", "emails", 2, 3, true, false, ""},
	{CapPerWeek, "audience", "Cross-brand cap · per person per week", "emails", 6, 8, true, false, ""},
	{SameBrandGapHours, "audience", "Same brand · minimum gap", "hours", 16, 12, false, false, ""},
	{ApprovalAbove, "audience", "Approval needed above", "people", 10000, 25000, true, false, "Sends to more people need a second admin."},
	{TestSlicePct, "audience", "Test slice · share", "%", 5, 2, false, false, "Every send starts with this share of the audience."},
	{TestSliceHoldMinutes, "audience", "Test slice · hold", "minutes", 45, 30, false, false, "How long the test slice is watched before the rest goes out."},
	{ImportInvalidPct, "imports", "Invalid addresses → admin review above", "%", 2, 5, true, false, ""},
	{ImportRoleDisposablePct, "imports", "Role + disposable → admin review above", "%", 5, 10, true, false, ""},
	{ImportQuarantineMonths, "imports", "Quarantine if no activity for", "months", 12, 24, true, false, ""},
	{RepermissionEmailsPerDay, "imports", "Re-permission emails per day", "emails", 200, 500, true, false, ""},
}

func Find(key string) (Def, bool) {
	i := slices.IndexFunc(Defs, func(d Def) bool { return d.Key == key })
	if i < 0 {
		return Def{}, false
	}
	return Defs[i], true
}

type Direction int

const (
	Same Direction = iota
	Tighter
	Looser
)

// Compare says whether moving from → to tightens or loosens the limit.
func (d Def) Compare(from, to float64) Direction {
	switch {
	case to == from:
		return Same
	case (to > from) == d.LooserIsHigher:
		return Looser
	}
	return Tighter
}

// WithinHard: the hard limit can never be crossed.
func (d Def) WithinHard(v float64) bool {
	if d.LooserIsHigher {
		return v <= d.Hard
	}
	return v >= d.Hard
}

type Service struct {
	store     *store.Store
	approvals *approvals.Service
	log       *slog.Logger
}

func NewService(st *store.Store, ap *approvals.Service, log *slog.Logger) *Service {
	s := &Service{store: st, approvals: ap, log: log}
	ap.Register(approvals.KindSafetyLimit, approvals.Definition{
		Label: "Loosen a safety limit", CompanyLevel: true, Execute: s.executeLoosen,
	})
	return s
}

// Limit is a definition with its current value.
type Limit struct {
	Def
	Current   float64    `json:"value"`
	ChangedBy string     `json:"changed_by"`
	ChangedAt *time.Time `json:"changed_at"`
}

func (s *Service) All(ctx context.Context) ([]Limit, error) {
	ov, err := s.store.LimitOverrides(ctx)
	if err != nil {
		return nil, err
	}
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	names := map[uuid.UUID]string{}
	for _, u := range users {
		names[u.ID] = u.Name
	}
	out := make([]Limit, 0, len(Defs))
	for _, d := range Defs {
		l := Limit{Def: d, Current: d.Value}
		if o, ok := ov[d.Key]; ok {
			l.Current, l.ChangedAt = o.Value, &o.UpdatedAt
			if o.UpdatedBy != nil {
				l.ChangedBy = names[*o.UpdatedBy]
			}
		}
		out = append(out, l)
	}
	return out, nil
}

// Get returns a limit's current value (the default if unchanged or on error).
func (s *Service) Get(ctx context.Context, key string) float64 {
	d, ok := Find(key)
	if !ok {
		return math.NaN()
	}
	ov, err := s.store.LimitOverrides(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "reading safety limits; using default", "key", key, "err", err)
		return d.Value
	}
	if o, ok := ov[key]; ok {
		return o.Value
	}
	return d.Value
}

type loosenPayload struct {
	Key  string  `json:"key"`
	From float64 `json:"from"`
	To   float64 `json:"to"`
}

func fmtValue(d Def, v float64) string {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if d.Unit == "%" {
		return s + "%"
	}
	return s + " " + d.Unit
}

// ChangeResult: what took effect now and what's waiting for a second admin.
type ChangeResult struct {
	Applied   []string         `json:"applied"`
	Requested []store.Approval `json:"requested"`
}

// Change applies tightenings at once and files an approval per loosening.
func (s *Service) Change(ctx context.Context, r *http.Request, p *auth.Principal, changes map[string]float64, reason string) (ChangeResult, error) {
	res := ChangeResult{Applied: []string{}, Requested: []store.Approval{}}
	current, err := s.All(ctx)
	if err != nil {
		return res, err
	}
	byKey := map[string]Limit{}
	for _, l := range current {
		byKey[l.Key] = l
	}
	for key, to := range changes {
		l, ok := byKey[key]
		if !ok {
			return res, httpx.Errorf(http.StatusBadRequest, "unknown_limit", "unknown limit %q", key)
		}
		if math.IsNaN(to) || math.IsInf(to, 0) || to <= 0 {
			return res, httpx.Errorf(http.StatusBadRequest, "bad_value", "%s must be a positive number", l.Label)
		}
		if l.Locked && l.Compare(l.Current, to) == Looser {
			return res, httpx.Errorf(http.StatusBadRequest, "locked", "%s is locked: it can only be made stricter", l.Label)
		}
		if !l.WithinHard(to) {
			return res, httpx.Errorf(http.StatusBadRequest, "past_hard_limit", "%s can't go past the hard limit of %s", l.Label, fmtValue(l.Def, l.Hard))
		}
	}

	err = s.store.InTx(ctx, func(tx *store.Store) error {
		for _, key := range sortedKeys(changes) {
			l, to := byKey[key], changes[key]
			switch l.Compare(l.Current, to) {
			case Same:
				continue
			case Tighter:
				if err := tx.SetLimit(ctx, key, to, p.User.ID); err != nil {
					return err
				}
				if err := tx.Audit(ctx, auth.AuditEntry(r, "safety_limit.tightened", "safety_limit", key, nil),
					map[string]float64{"value": l.Current}, map[string]float64{"value": to}); err != nil {
					return err
				}
				res.Applied = append(res.Applied, key)
			case Looser:
				a, err := s.approvals.Request(ctx, tx, approvals.Request{
					Kind: approvals.KindSafetyLimit, RequestedBy: p.User, Reason: reason,
					Summary: fmt.Sprintf("Loosen %s: %s → %s", l.Label, fmtValue(l.Def, l.Current), fmtValue(l.Def, to)),
					Payload: loosenPayload{Key: key, From: l.Current, To: to},
					Audit:   auth.AuditEntry(r, "", "", "", nil),
				})
				if errors.Is(err, approvals.ErrNoApprover) {
					return httpx.Errorf(http.StatusConflict, "no_approver",
						"loosening %s needs a second admin, and there's no other owner or approver yet", l.Label)
				}
				if err != nil {
					return err
				}
				res.Requested = append(res.Requested, a)
			}
		}
		return nil
	})
	return res, err
}

func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// executeLoosen applies an approved loosening, re-checking the hard limit.
func (s *Service) executeLoosen(ctx context.Context, tx *store.Store, a store.Approval) (any, error) {
	var p loosenPayload
	if err := jsonUnmarshal(a.Payload, &p); err != nil {
		return nil, err
	}
	d, ok := Find(p.Key)
	if !ok || !d.WithinHard(p.To) || d.Locked {
		return nil, httpx.Errorf(http.StatusConflict, "past_hard_limit", "this change is no longer allowed")
	}
	if err := tx.SetLimit(ctx, p.Key, p.To, a.RequestedBy); err != nil {
		return nil, err
	}
	return map[string]any{"key": p.Key, "value": p.To}, nil
}

package health

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/brands"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/ses"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/warmup"
)

// Decider says whether someone may decide an approval (approvals.Service).
type Decider interface {
	CanDecide(p *auth.Principal, a store.Approval) bool
}

type Handlers struct {
	Service   *Service
	Store     *store.Store
	Approvals Decider
}

func (h *Handlers) Routes(mux *http.ServeMux) {
	f := func(fn httpx.HandlerFunc) http.HandlerFunc { return httpx.Handle(h.Service.log, fn) }
	mux.HandleFunc("GET /admin/v1/safety-center", f(h.safetyCenter))
	mux.HandleFunc("GET /admin/v1/brands/{slug}/health", f(h.brandHealth))
	mux.HandleFunc("POST /admin/v1/brands/{slug}/health/recheck", f(h.recheck))
}

// --- S1 Safety center ---

type brandCard struct {
	Slug       string          `json:"slug"`
	Name       string          `json:"name"`
	Color      string          `json:"color"`
	Status     string          `json:"status"`
	Score      *int            `json:"score"`
	Level      string          `json:"level"`
	Weakest    string          `json:"weakest"`
	Components []componentLite `json:"components"`
	ComputedAt *time.Time      `json:"computed_at"`
	Warmup     warmup.Status   `json:"warmup"`
}

type componentLite struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Score *int   `json:"score"`
}

type accountMeters struct {
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
	Region    string `json:"region"`
	ses.Account
	// MarketingSentToday: what this app sent today (UTC), all brands.
	MarketingSentToday int `json:"marketing_sent_today"`
}

// Attention is one "Needs attention" row.
type Attention struct {
	Severity string `json:"severity"` // critical | warning | info
	Kind     string `json:"kind"`
	Brand    string `json:"brand,omitempty"` // slug
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Link     string `json:"link,omitempty"`
}

type protection struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	On     bool   `json:"on"`
	Detail string `json:"detail"`
}

type safetyCenter struct {
	Brands         []brandCard   `json:"brands"`
	Account        accountMeters `json:"account"`
	NeedsAttention []Attention   `json:"needs_attention"`
	Protections    []protection  `json:"protections"`
}

func (h *Handlers) safetyCenter(w http.ResponseWriter, r *http.Request) error {
	p, err := auth.Require(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	s := h.Service
	all, err := h.Store.ListBrands(ctx)
	if err != nil {
		return err
	}
	saved, err := h.Store.AllHealth(ctx)
	if err != nil {
		return err
	}
	today := s.now().UTC()
	out := safetyCenter{Brands: []brandCard{}, NeedsAttention: []Attention{}}

	for _, b := range all {
		if !p.CanViewBrand(b.ID) {
			continue
		}
		res, at, err := s.stored(ctx, b, saved)
		if err != nil {
			return err
		}
		wu, err := s.warm.StatusOn(ctx, nil, b.ID, today)
		if err != nil {
			return err
		}
		card := brandCard{Slug: b.Slug, Name: b.Name, Color: b.Color, Status: string(b.Status),
			Score: res.Score, Level: res.Level, Weakest: res.Weakest, ComputedAt: at, Warmup: wu}
		for _, c := range res.Components {
			card.Components = append(card.Components, componentLite{Key: c.Key, Label: c.Label, Score: c.Score})
		}
		out.Brands = append(out.Brands, card)
		out.NeedsAttention = append(out.NeedsAttention, brandAttention(b, res)...)
	}

	// Shared SES account.
	acc, err := s.Account(ctx)
	out.Account = accountMeters{Available: err == nil, Region: s.ses.Region(), Account: acc}
	if err != nil {
		out.Account.Error = "Couldn't read the SES account"
	}
	if out.Account.MarketingSentToday, err = h.Store.SentAllBrandsOn(ctx, today); err != nil {
		return err
	}
	out.NeedsAttention = append(out.NeedsAttention, accountAttention(out.Account)...)

	// Approvals waiting for this person.
	waiting, err := h.Store.ListApprovals(ctx, store.ApprovalFilter{DecidableBy: &p.User.ID, Limit: 200})
	if err != nil {
		return err
	}
	n := 0
	for _, a := range waiting {
		if h.Approvals.CanDecide(p, a) {
			n++
		}
	}
	if n > 0 {
		out.NeedsAttention = append(out.NeedsAttention, Attention{Severity: "warning", Kind: "approvals",
			Title:  plural(n, "request is", "requests are") + " waiting for your approval",
			Detail: "Nothing risky happens until a second person says yes.", Link: "/approvals"})
	}

	// Open alerts (owners and approvers see them).
	if p.SeesAllBrands() {
		alerts, err := h.Store.ListAlerts(ctx, true, 20)
		if err != nil {
			return err
		}
		for _, a := range alerts {
			sev := a.Severity
			if sev != "critical" && sev != "warning" {
				sev = "info"
			}
			att := Attention{Severity: sev, Kind: "alert", Title: a.Message, Detail: "Alert from " + a.CreatedAt.UTC().Format("Jan 2, 15:04 UTC")}
			if a.BrandID != nil {
				for _, b := range all {
					if b.ID == *a.BrandID {
						att.Brand = b.Slug
					}
				}
			}
			out.NeedsAttention = append(out.NeedsAttention, att)
		}
	}

	if p.IsOwner() {
		if !s.PostmasterConnected() {
			out.NeedsAttention = append(out.NeedsAttention, Attention{Severity: "info", Kind: "postmaster_not_connected",
				Title:  "Gmail Postmaster Tools isn't connected",
				Detail: "It's the only way to see Gmail's spam rate for your domains. Set POSTMASTER_CREDENTIALS_FILE."})
		}
		if !s.BlocklistsOn() {
			out.NeedsAttention = append(out.NeedsAttention, Attention{Severity: "info", Kind: "blocklists_off",
				Title: "Blocklist checks are off", Detail: "Set BLOCKLIST_RESOLVER to your own DNS resolver to turn them on."})
		}
	}

	sevRank := map[string]int{"critical": 0, "warning": 1, "info": 2}
	slices.SortStableFunc(out.NeedsAttention, func(a, b Attention) int { return sevRank[a.Severity] - sevRank[b.Severity] })

	if out.Protections, err = h.protections(r); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func brandAttention(b store.Brand, r Result) []Attention {
	var out []Attention
	settings := "/settings/brands/" + b.Slug
	health := "/b/" + b.Slug + "/health"
	if b.Status == store.BrandPaused {
		out = append(out, Attention{Severity: "critical", Kind: "brand_paused", Brand: b.Slug,
			Title: b.Name + " is paused", Detail: "All marketing email for this brand is stopped.", Link: health})
	}
	var broken []string
	for _, c := range r.Components {
		for _, ch := range c.Checks {
			switch {
			case c.Key == "authentication" && ch.Status == Failing:
				broken = append(broken, ch.Label)
			case c.Key == "authentication" && ch.Label == "DMARC" && ch.Status == Warning && b.Status == store.BrandReady:
				out = append(out, Attention{Severity: "info", Kind: "dmarc_none", Brand: b.Slug,
					Title: b.Name + ": tighten DMARC", Detail: ch.Detail, Link: settings})
			case ch.Label == "Blocklists" && ch.Status == Failing:
				out = append(out, Attention{Severity: "critical", Kind: "blocklisted", Brand: b.Slug,
					Title: b.Name + " is on a blocklist", Detail: ch.Detail + ". Find what caused it, then ask the list to remove it.", Link: health})
			case c.Key == "reputation" && ch.Label == "Gmail spam rate" && ch.Status != OK:
				out = append(out, Attention{Severity: map[string]string{Failing: "critical", Warning: "warning"}[ch.Status], Kind: "gmail_spam", Brand: b.Slug,
					Title: b.Name + ": Gmail spam rate is " + ch.Detail, Link: health})
			}
		}
	}
	if len(broken) > 0 {
		// One row per brand, prepended: nothing else matters until it can send.
		out = append([]Attention{{Severity: "critical", Kind: "dns_broken", Brand: b.Slug,
			Title:  b.Name + " can't send: DNS isn't right",
			Detail: "Not passing: " + strings.Join(broken, ", ") + ". Sending is blocked until it is.", Link: settings}}, out...)
	}
	if r.Level == "at_risk" && len(broken) == 0 { // broken DNS already explains it
		out = append(out, Attention{Severity: "warning", Kind: "low_health", Brand: b.Slug,
			Title: fmt.Sprintf("%s health is %d", b.Name, *r.Score), Detail: r.Weakest, Link: health})
	}
	return out
}

func accountAttention(a accountMeters) []Attention {
	if !a.Available {
		return []Attention{{Severity: "warning", Kind: "ses_unavailable", Title: "Couldn't read the SES account", Detail: "Check the app's AWS permissions (ses:GetAccount)."}}
	}
	var out []Attention
	if !a.SendingEnabled {
		out = append(out, Attention{Severity: "critical", Kind: "ses_sending_disabled", Title: "SES sending is turned off for the whole account",
			Detail: "No brand can send. Check the AWS console for a notice from Amazon."})
	}
	switch a.EnforcementStatus {
	case "", "HEALTHY":
	case "PROBATION":
		out = append(out, Attention{Severity: "critical", Kind: "ses_probation", Title: "Amazon put the SES account on probation",
			Detail: "Bounces or complaints are too high across brands. Fix it before Amazon pauses sending."})
	default:
		out = append(out, Attention{Severity: "critical", Kind: "ses_enforcement", Title: "SES account status: " + a.EnforcementStatus})
	}
	if !a.ProductionAccess {
		out = append(out, Attention{Severity: "warning", Kind: "ses_sandbox", Title: "SES is still in the sandbox",
			Detail: "Only verified addresses receive mail. Request production access in the AWS console."})
	}
	if a.Max24HourSend > 0 && a.SentLast24Hours/a.Max24HourSend >= 0.8 {
		out = append(out, Attention{Severity: "warning", Kind: "ses_quota", Title: "SES daily quota is almost used up",
			Detail: fmt.Sprintf("%.0f of %.0f emails in the last 24 hours.", a.SentLast24Hours, a.Max24HourSend)})
	}
	return out
}

func (h *Handlers) protections(r *http.Request) ([]protection, error) {
	ctx := r.Context()
	twoAdmins, err := h.Store.HadTwoAdmins(ctx)
	if err != nil {
		return nil, err
	}
	suppressed, err := h.Store.SuppressionCount(ctx)
	if err != nil {
		return nil, err
	}
	s := h.Service
	out := []protection{
		{Key: "two_person", Label: "Two-person approvals", On: twoAdmins,
			Detail: map[bool]string{true: "Risky actions need a second admin.", false: "Only one admin so far: risky actions go through alone. Invite a second admin."}[twoAdmins]},
		{Key: "suppression", Label: "Company-wide suppression", On: true,
			Detail: fmt.Sprintf("%d addresses are never emailed by any brand.", suppressed)},
		{Key: "passkeys", Label: "Passkey sign-in", On: true, Detail: "No passwords to steal or phish."},
		{Key: "access", Label: "Cloudflare Access", On: s.cfg.AccessOn,
			Detail: map[bool]string{true: "The admin app is only reachable through Cloudflare Access.", false: "Not configured (fine for local development)."}[s.cfg.AccessOn]},
		{Key: "blocklists", Label: "Blocklist monitoring", On: s.BlocklistsOn(),
			Detail: map[bool]string{true: "Spamhaus DBL, SURBL and URIBL, every few hours.", false: "Off: set BLOCKLIST_RESOLVER."}[s.BlocklistsOn()]},
		{Key: "postmaster", Label: "Gmail Postmaster Tools", On: s.PostmasterConnected(),
			Detail: map[bool]string{true: "Gmail spam rate and reputation, daily.", false: "Not connected."}[s.PostmasterConnected()]},
	}
	return out, nil
}

// --- S2 Brand health ---

type postmasterView struct {
	Connected bool                   `json:"connected"`
	Status    string                 `json:"status"` // not_connected | waiting | ok | domain_not_found | error
	Detail    string                 `json:"detail"`
	History   []store.PostmasterStat `json:"history"` // last 30 days, oldest first
}

type brandHealth struct {
	Brand      store.Brand             `json:"brand"`
	Health     Result                  `json:"health"`
	ComputedAt *time.Time              `json:"computed_at"`
	Warmup     warmup.Status           `json:"warmup"`
	DNSRecords []store.DNSRecord       `json:"dns_records"`
	Blocklists []store.BlocklistResult `json:"blocklists"`
	// BlocklistsOn false = checks are off (BLOCKLIST_RESOLVER=off).
	BlocklistsOn bool           `json:"blocklists_on"`
	Postmaster   postmasterView `json:"postmaster"`
	Events       []store.Alert  `json:"events"`
}

func (h *Handlers) brandHealth(w http.ResponseWriter, r *http.Request) error {
	b, _, err := brands.Visible(r, h.Store)
	if err != nil {
		return err
	}
	saved, err := h.Store.AllHealth(r.Context())
	if err != nil {
		return err
	}
	return h.writeHealth(w, r, b, saved)
}

func (h *Handlers) recheck(w http.ResponseWriter, r *http.Request) error {
	b, _, err := brands.Visible(r, h.Store)
	if err != nil {
		return err
	}
	if b, err = h.Service.Recheck(r.Context(), b); err != nil {
		return err
	}
	saved, err := h.Store.AllHealth(r.Context())
	if err != nil {
		return err
	}
	return h.writeHealth(w, r, b, saved)
}

func (h *Handlers) writeHealth(w http.ResponseWriter, r *http.Request, b store.Brand, saved map[uuid.UUID]store.BrandHealth) error {
	ctx := r.Context()
	s := h.Service
	res, at, err := s.stored(ctx, b, saved)
	if err != nil {
		return err
	}
	out := brandHealth{Brand: b, Health: res, ComputedAt: at, BlocklistsOn: s.BlocklistsOn()}
	if out.Warmup, err = s.warm.StatusOn(ctx, nil, b.ID, s.now()); err != nil {
		return err
	}
	if out.DNSRecords, err = h.Store.DNSRecords(ctx, b.ID); err != nil {
		return err
	}
	if out.Blocklists, err = h.Store.BlocklistResults(ctx, b.ID); err != nil {
		return err
	}
	pm := postmasterView{Connected: s.PostmasterConnected(), Status: "not_connected"}
	if pm.Connected {
		pm.Status = "waiting"
		if st, err := h.Store.GetPostmasterStatus(ctx, b.ID); err == nil {
			pm.Status, pm.Detail = st.Status, st.Detail
		}
		if pm.History, err = h.Store.PostmasterHistory(ctx, b.ID, s.now().UTC().AddDate(0, 0, -30)); err != nil {
			return err
		}
	}
	out.Postmaster = pm
	if out.Events, err = h.Store.BrandAlerts(ctx, b.ID, 10); err != nil {
		return err
	}
	if out.DNSRecords == nil {
		out.DNSRecords = []store.DNSRecord{}
	}
	if out.Blocklists == nil {
		out.Blocklists = []store.BlocklistResult{}
	}
	if out.Postmaster.History == nil {
		out.Postmaster.History = []store.PostmasterStat{}
	}
	if out.Events == nil {
		out.Events = []store.Alert{}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

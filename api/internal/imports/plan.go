package imports

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// Consent is the importer's answer to "where did these contacts come from?".
// Bought, rented, scraped and "I don't know" are deliberately not options.
type Consent struct {
	Source          string `json:"source"`        // see sourceLabels
	SourceDetail    string `json:"source_detail"` // e.g. "Mailchimp export"
	CollectedPeriod string `json:"collected_period"`
	Wording         string `json:"wording"`     // consent text or signup page URL
	LegalBasis      string `json:"legal_basis"` // consent | soft_opt_in
	DoubleOptIn     bool   `json:"double_opt_in"`
	// Tracking: who agreed to click tracking. "all", "none", "column" (per
	// row, from Mapping.Tracking) or "eu_by_country" (default: nobody in the
	// EU/EEA/UK, everyone else yes). Without it, people are tiered from their
	// opt-in date only and are never Active.
	Tracking string `json:"tracking"`

	Attested        bool       `json:"attested"`
	AttestedBy      *uuid.UUID `json:"attested_by,omitempty"`
	AttestedByEmail string     `json:"attested_by_email,omitempty"`
	AttestedAt      *time.Time `json:"attested_at,omitempty"`
}

// AttestationText is what the importer signs; stored with the import and in every contact's consent record.
const AttestationText = "I confirm this list was not bought, rented, appended, scraped or shared by anyone else, and everyone on it agreed to hear from this brand."

var sourceLabels = map[string]string{
	"platform_migration": "Moving from another platform",
	"website_form":       "Signup form on our website",
	"checkout":           "Opt-in at checkout",
	"event":              "In person or at an event",
	"customers":          "Existing customers (soft opt-in)",
}

func (c Consent) Label() string {
	label := sourceLabels[c.Source]
	if c.SourceDetail != "" {
		label += " — " + c.SourceDetail
	}
	return label
}

// Problems lists what's missing before the import may run. Empty = complete.
func (c Consent) Problems() []string {
	var p []string
	if sourceLabels[c.Source] == "" {
		p = append(p, "choose where these contacts came from")
	}
	if c.Source == "platform_migration" && strings.TrimSpace(c.SourceDetail) == "" {
		p = append(p, "name the platform they're coming from")
	}
	if strings.TrimSpace(c.CollectedPeriod) == "" {
		p = append(p, "say when they were collected")
	}
	if strings.TrimSpace(c.Wording) == "" {
		p = append(p, "add the consent wording or signup page URL")
	}
	switch c.LegalBasis {
	case "consent":
	case "soft_opt_in":
		if c.Source != "customers" && c.Source != "checkout" {
			p = append(p, "soft opt-in only applies to existing customers")
		}
	default:
		p = append(p, "choose the legal basis")
	}
	switch c.Tracking {
	case "", "all", "none", "column", "eu_by_country":
	default:
		p = append(p, "choose who agreed to click tracking")
	}
	if !c.Attested {
		p = append(p, "confirm the list wasn't bought, rented or scraped")
	}
	return p
}

// euCountries need click-tracking consent (GDPR / UK GDPR + ePrivacy): EU, EEA and the UK.
var euCountries = map[string]bool{
	"AT": true, "BE": true, "BG": true, "HR": true, "CY": true, "CZ": true, "DK": true, "EE": true, "FI": true,
	"FR": true, "DE": true, "GR": true, "EL": true, "HU": true, "IE": true, "IT": true, "LV": true, "LT": true,
	"LU": true, "MT": true, "NL": true, "PL": true, "PT": true, "RO": true, "SK": true, "SI": true, "ES": true,
	"SE": true, "IS": true, "LI": true, "NO": true, "GB": true, "UK": true,
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "yes", "y", "true", "1", "granted", "agreed", "opted in", "opt-in", "optin", "subscribed":
		return true
	}
	return false
}

// ResolveTracking decides each row's click-tracking consent.
func ResolveTracking(rows []Row, mode string) {
	for i := range rows {
		r := &rows[i]
		switch mode {
		case "all":
			r.TrackingAllowed = true
		case "none":
			r.TrackingAllowed = false
		case "column":
			r.TrackingAllowed = truthy(r.TrackingRaw)
		default: // eu_by_country
			r.TrackingAllowed = !euCountries[r.Country]
		}
	}
}

func (c Consent) basis() string {
	switch {
	case c.DoubleOptIn:
		return "double_opt_in"
	case c.LegalBasis == "soft_opt_in":
		return "soft_opt_in"
	}
	return "single_opt_in"
}

// Row is one cleaned CSV row.
type Row struct {
	Line          int // 2 = first data row (line 1 is the header)
	Email         string
	FirstName     string
	LastName      string
	Tags          []string
	ConsentedAt   *time.Time
	LastEngagedAt *time.Time
	Rating        int // 1–5 stars from the old tool, 0 = unknown
	TrackingRaw   string
	// TrackingAllowed: set by ResolveTracking. Without it, clicks and stars
	// don't count; only the opt-in date does.
	TrackingAllowed bool
	Timezone        string
	Country         string
	Outcome         Outcome
	ExistingID      uuid.UUID
}

// ClockSkew: dates up to a day ahead are accepted (time zones, clock drift).
const ClockSkew = 24 * time.Hour

// Rows extracts mapped values; Email is normalized ("" if invalid). Dates in
// the future count as no date: they'd keep someone "Active" for years.
func Rows(t Table, m Mapping, now time.Time) []Row {
	past := func(d *time.Time) *time.Time {
		if d == nil || d.After(now.Add(ClockSkew)) {
			return nil
		}
		return d
	}
	out := make([]Row, 0, len(t.Rows))
	for i, rec := range t.Rows {
		out = append(out, Row{
			Line:          i + 2,
			Email:         NormalizeEmail(t.Get(rec, m.Email)),
			FirstName:     truncate(t.Get(rec, m.FirstName), 100),
			LastName:      truncate(t.Get(rec, m.LastName), 100),
			Tags:          ParseTags(t.Get(rec, m.Tags)),
			ConsentedAt:   past(ParseDate(t.Get(rec, m.ConsentedAt))),
			LastEngagedAt: past(ParseDate(t.Get(rec, m.LastEngagedAt))),
			Rating:        parseRating(t.Get(rec, m.Rating)),
			TrackingRaw:   t.Get(rec, m.Tracking),
			Timezone:      truncate(t.Get(rec, m.Timezone), 64),
			Country:       strings.ToUpper(truncate(t.Get(rec, m.Country), 2)),
		})
	}
	return out
}

// EngagedRating is the lowest star rating that counts as recent activity.
// Mailchimp only gives 4–5 stars to people who opened or clicked lately.
const EngagedRating = 4

// Engaged: a 4–5 star rating counts like a click at import time — but only
// for people who agreed to tracking (the rating comes from tracked opens/clicks).
func (r Row) Engaged() bool { return r.Rating >= EngagedRating && r.TrackingAllowed }

func parseRating(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 || n > 5 {
		return 0
	}
	return n
}

// truncate keeps at most n characters (runes, so multi-byte text isn't cut mid-character).
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// Facts are what we know about the addresses from the database and DNS.
type Facts struct {
	Suppressed map[string]bool          // by email
	Existing   map[string]store.Contact // by email
	NoMailbox  map[string]bool          // by domain
	Now        time.Time
}

// Classify decides every row's outcome. First matching rule wins.
func Classify(rows []Row, f Facts) {
	seen := map[string]bool{}
	for i := range rows {
		r := &rows[i]
		switch {
		case r.Email == "":
			r.Outcome = Invalid
		case seen[r.Email]:
			r.Outcome = Duplicate
		case f.Suppressed[r.Email]:
			r.Outcome = Suppressed
		case isRole(r.Email):
			r.Outcome = Role
		case isDisposable(r.Email):
			r.Outcome = Disposable
		case f.NoMailbox[domainOf(r.Email)]:
			r.Outcome = NoMailbox
		default:
			if c, ok := f.Existing[r.Email]; ok {
				r.ExistingID = c.ID
				if c.Status == store.ContactUnsubscribed {
					r.Outcome = Unsubscribed
				} else {
					r.Outcome = Existing
				}
			} else if stale(r, f.Now) {
				r.Outcome = Quarantine
			} else {
				r.Outcome = Ready
			}
		}
		if r.Email != "" {
			seen[r.Email] = true
		}
	}
}

// stale: no sign of life in the last year (or no dates at all) → quarantine.
// A 4–5 star rating is a sign of life.
func stale(r *Row, now time.Time) bool {
	if r.Engaged() {
		return false
	}
	last := r.LastEngagedAt
	if !r.TrackingAllowed {
		last = nil // no tracking consent: only the opt-in counts
	}
	if r.ConsentedAt != nil && (last == nil || r.ConsentedAt.After(*last)) {
		last = r.ConsentedAt
	}
	return last == nil || now.Sub(*last) > QuarantineAfter
}

// CheckResult is shown on S4's right-hand panel and stored with the import.
type CheckResult struct {
	Total            int                  `json:"total"`
	Counts           map[Outcome]int      `json:"counts"`
	Samples          map[Outcome][]string `json:"samples"` // a few addresses per outcome
	NoMailboxDomains []string             `json:"no_mailbox_domains"`
	NoTracking       int                  `json:"no_tracking"` // importable rows without click-tracking consent
	DatesMapped      bool                 `json:"dates_mapped"`
	Gate             Gate                 `json:"gate"`
	CheckedAt        time.Time            `json:"checked_at"`
}

type Gate struct {
	InvalidRate         float64  `json:"invalid_rate"`
	InvalidLimit        float64  `json:"invalid_limit"`
	RoleDisposableRate  float64  `json:"role_disposable_rate"`
	RoleDisposableLimit float64  `json:"role_disposable_limit"`
	ConsentRecorded     bool     `json:"consent_recorded"`
	Passed              bool     `json:"passed"`
	Problems            []string `json:"problems"`
}

func Summarize(rows []Row, c Consent, m Mapping, noMailbox []string, now time.Time) CheckResult {
	res := CheckResult{
		Total: len(rows), Counts: map[Outcome]int{}, Samples: map[Outcome][]string{},
		NoMailboxDomains: noMailbox, DatesMapped: m.ConsentedAt != "" || m.LastEngagedAt != "" || m.Rating != "", CheckedAt: now,
	}
	if res.NoMailboxDomains == nil {
		res.NoMailboxDomains = []string{}
	}
	for _, r := range rows {
		res.Counts[r.Outcome]++
		if importable(r.Outcome) && !r.TrackingAllowed {
			res.NoTracking++
		}
		if len(res.Samples[r.Outcome]) < 5 {
			sample := r.Email
			if sample == "" {
				sample = "line " + strconv.Itoa(r.Line)
			}
			res.Samples[r.Outcome] = append(res.Samples[r.Outcome], sample)
		}
	}
	total := float64(max(len(rows), 1))
	g := Gate{
		InvalidRate:         float64(res.Counts[Invalid]+res.Counts[NoMailbox]) / total,
		InvalidLimit:        InvalidLimit,
		RoleDisposableRate:  float64(res.Counts[Role]+res.Counts[Disposable]) / total,
		RoleDisposableLimit: RoleDisposableLimit,
		Problems:            c.Problems(),
	}
	g.ConsentRecorded = len(g.Problems) == 0
	if g.InvalidRate > g.InvalidLimit {
		g.Problems = append(g.Problems, "too many invalid addresses: a list this broken usually wasn't collected with consent")
	}
	if g.RoleDisposableRate > g.RoleDisposableLimit {
		g.Problems = append(g.Problems, "too many role or throwaway addresses")
	}
	if res.Counts[Ready]+res.Counts[Quarantine]+res.Counts[Existing] == 0 {
		g.Problems = append(g.Problems, "nobody in this file can be imported")
	}
	if g.Problems == nil {
		g.Problems = []string{}
	}
	g.Passed = len(g.Problems) == 0
	res.Gate = g
	return res
}

var errGateFailed = errors.New("the import didn't pass the safety check")

// importable: rows that create or update a contact.
func importable(o Outcome) bool {
	return slices.Contains([]Outcome{Ready, Quarantine, Existing}, o)
}

// Package health computes each brand's health score (S2) and the company
// Safety center (S1).
//
// The score is 0–100 from four parts, each 0–100:
//
//	authentication 25%  DNS (DKIM, SPF, DMARC, MAIL FROM) and the SES identity
//	reputation     35%  blocklists, Gmail Postmaster, bounces and complaints
//	list quality   20%  quarantined/suppressed share, invalid rate of imports
//	engagement     20%  engagement tiers, recent unsubscribes
//
// A part with no data yet (a new brand has no contacts) is left out and the
// weights of the others are scaled up, so the score never punishes "unknown".
// Reputation takes its worst signal: one blocklist listing is enough to send
// mail to spam, however good the rest is.
package health

import (
	"fmt"
	"math"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// Check statuses, shared with DNS records.
const (
	OK      = "ok"
	Warning = "warning"
	Failing = "failing"
	Unknown = "unknown"
)

// Check is one line in a part's breakdown (S2).
type Check struct {
	Label  string `json:"label"`
	Status string `json:"status"` // ok | warning | failing | unknown
	Detail string `json:"detail"`
}

// Component is one of the four parts.
type Component struct {
	Key     string  `json:"key"`
	Label   string  `json:"label"`
	Weight  float64 `json:"weight"`
	Score   *int    `json:"score"` // nil = no data yet
	Summary string  `json:"summary"`
	Checks  []Check `json:"checks"`
}

// Result is a brand's score. Score is nil when no part has data.
type Result struct {
	Score      *int        `json:"score"`
	Level      string      `json:"level"` // healthy | watch | at_risk | unknown
	Weakest    string      `json:"weakest"`
	Components []Component `json:"components"`
}

// Level: 80+ healthy, 60–79 watch, below 60 at risk.
func Level(score *int) string {
	switch {
	case score == nil:
		return "unknown"
	case *score >= 80:
		return "healthy"
	case *score >= 60:
		return "watch"
	default:
		return "at_risk"
	}
}

// ReputationSignals come from the brand's own sending (M4 plugs these in via
// a ReputationSource). Rates are fractions: 0.01 = 1%.
type ReputationSignals struct {
	Sent7d          int
	BounceRate7d    float64
	ComplaintRate7d float64
}

// Inputs is everything the score is computed from.
type Inputs struct {
	Brand      store.Brand
	DNS        []store.DNSRecord
	Blocklists []store.BlocklistResult
	Postmaster *store.PostmasterStat // latest day, nil = none
	Signals    *ReputationSignals    // nil = no source / not enough sending
	Contacts   store.ContactStats
	// ImportInvalidRate: average invalid share of recent imports (nil = none).
	ImportInvalidRate *float64
	Unsubscribes30d   int
}

// MinSentForRates: below this, bounce and complaint rates are too noisy to score.
const MinSentForRates = 200

func ptr(n int) *int { return &n }

func clamp(f float64) int { return int(math.Round(math.Max(0, math.Min(100, f)))) }

func pct(f float64) string {
	switch p := f * 100; {
	case p == 0:
		return "0%"
	case p < 0.1:
		return fmt.Sprintf("%.3f%%", p)
	case p < 10:
		return fmt.Sprintf("%.1f%%", p)
	default:
		return fmt.Sprintf("%.0f%%", p)
	}
}

// Compute scores a brand.
func Compute(in Inputs) Result {
	comps := []Component{authentication(in), reputation(in), listQuality(in), engagement(in)}
	var sum, weights float64
	for _, c := range comps {
		if c.Score != nil {
			sum += float64(*c.Score) * c.Weight
			weights += c.Weight
		}
	}
	r := Result{Components: comps}
	if weights > 0 {
		r.Score = ptr(clamp(sum / weights))
		var weakest *Component
		for i := range comps {
			c := &comps[i]
			if c.Score != nil && *c.Score < 80 && (weakest == nil || *c.Score < *weakest.Score) {
				weakest = c
			}
		}
		if weakest != nil {
			r.Weakest = weakest.Label + ": " + weakest.Summary
		}
	}
	r.Level = Level(r.Score)
	return r
}

var purposeLabel = map[string]string{
	"dkim": "DKIM", "mail_from_mx": "MAIL FROM MX", "mail_from_spf": "SPF", "dmarc": "DMARC",
}

func authentication(in Inputs) Component {
	c := Component{Key: "authentication", Label: "Authentication", Weight: 0.25}
	if len(in.DNS) == 0 {
		c.Summary = "No DNS records yet."
		return c
	}
	var sum float64
	dkimSeen := false
	failing, warning := 0, 0
	for _, r := range in.DNS {
		switch r.Status {
		case OK:
			sum += 100
		case Warning:
			sum += 70
			warning++
		case "pending":
			sum += 30
		default:
			failing++
		}
		// One line for DKIM (three CNAMEs), one per other record.
		if r.Purpose == "dkim" {
			if dkimSeen {
				continue
			}
			dkimSeen = true
			st := OK
			for _, d := range in.DNS {
				if d.Purpose == "dkim" && d.Status != OK {
					st = worse(st, dnsStatus(d.Status))
				}
			}
			c.Checks = append(c.Checks, Check{Label: "DKIM", Status: st, Detail: "Signs every email so receivers know it's really from you"})
			continue
		}
		detail := r.Detail
		if r.Purpose == "dmarc" && r.Status == Warning {
			detail = "Policy is p=none. Once reports look clean, move to p=quarantine."
		}
		c.Checks = append(c.Checks, Check{Label: purposeLabel[r.Purpose], Status: dnsStatus(r.Status), Detail: detail})
	}
	score := sum / float64(len(in.DNS))
	sesOK := in.Brand.SESIdentityStatus == "SUCCESS"
	sesCheck := Check{Label: "SES identity", Status: OK, Detail: "Verified in Amazon SES"}
	if !sesOK {
		score = math.Min(score, 40)
		sesCheck = Check{Label: "SES identity", Status: Failing, Detail: "Not verified in Amazon SES yet (" + in.Brand.SESIdentityStatus + ")"}
	}
	c.Checks = append(c.Checks, sesCheck)
	c.Score = ptr(clamp(score))
	switch {
	case failing > 0 || !sesOK:
		c.Summary = "DNS isn't fully set up, so the brand can't send."
	case warning > 0:
		c.Summary = "Works, but DMARC is still on p=none."
	default:
		c.Summary = "DKIM, SPF and DMARC all pass."
	}
	return c
}

func dnsStatus(s string) string {
	switch s {
	case OK, Warning, Failing:
		return s
	}
	return Unknown // pending
}

var rank = map[string]int{OK: 0, Unknown: 1, Warning: 2, Failing: 3}

func worse(a, b string) string {
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func reputation(in Inputs) Component {
	c := Component{Key: "reputation", Label: "Reputation", Weight: 0.35}
	var scores []int
	var listed []string

	// Blocklists.
	if len(in.Blocklists) > 0 {
		st, clean, unknown := OK, 0, 0
		for _, b := range in.Blocklists {
			switch b.Status {
			case "listed":
				listed = append(listed, b.Domain+" on "+b.Zone)
				st = Failing
			case "clean":
				clean++
			default:
				unknown++
			}
		}
		detail := fmt.Sprintf("Not listed (%d checks)", clean)
		switch {
		case len(listed) > 0:
			detail = "Listed: " + joinMax(listed, 3)
			scores = append(scores, 0)
		case clean > 0:
			scores = append(scores, 100)
			if unknown > 0 {
				detail += fmt.Sprintf(", %d couldn't be checked", unknown)
			}
		default:
			st, detail = Unknown, "Lookups were refused or failed; set BLOCKLIST_RESOLVER to your own DNS resolver"
		}
		c.Checks = append(c.Checks, Check{Label: "Blocklists", Status: st, Detail: detail})
	} else {
		c.Checks = append(c.Checks, Check{Label: "Blocklists", Status: Unknown, Detail: "Not checked yet"})
	}

	// Gmail (Postmaster Tools).
	if p := in.Postmaster; p != nil {
		if rep := p.DomainReputation; rep != "" {
			s := map[string]int{"HIGH": 100, "MEDIUM": 70, "LOW": 30, "BAD": 0}[rep]
			scores = append(scores, s)
			c.Checks = append(c.Checks, Check{Label: "Gmail domain reputation", Status: map[bool]string{true: OK, false: Warning}[s >= 70], Detail: rep + " on " + p.Day.Format("Jan 2")})
			if s < 30 {
				c.Checks[len(c.Checks)-1].Status = Failing
			}
		}
		if p.SpamRatio != nil {
			r := *p.SpamRatio
			s, st := 100, OK
			switch {
			case r >= 0.003:
				s, st = 0, Failing
			case r >= 0.001:
				s, st = 50, Warning
			}
			scores = append(scores, s)
			c.Checks = append(c.Checks, Check{Label: "Gmail spam rate", Status: st, Detail: pct(r) + " (keep under 0.1%; Gmail blocks at 0.3%)"})
		}
	}

	// Own sending (M4).
	if s := in.Signals; s != nil && s.Sent7d >= MinSentForRates {
		bs, bst := band(s.BounceRate7d, 0.01, 0.02, 0.03)
		cs, cst := band(s.ComplaintRate7d, 0.0005, 0.0007, 0.0008)
		scores = append(scores, bs, cs)
		c.Checks = append(c.Checks,
			Check{Label: "Bounces (7 days)", Status: bst, Detail: pct(s.BounceRate7d)},
			Check{Label: "Complaints (7 days)", Status: cst, Detail: pct(s.ComplaintRate7d)})
	}

	if len(scores) == 0 {
		c.Summary = "No reputation data yet."
		return c
	}
	worst := 100
	for _, s := range scores {
		worst = min(worst, s)
	}
	c.Score = ptr(worst)
	switch {
	case len(listed) > 0:
		c.Summary = "On a blocklist: mail is likely going to spam."
	case worst < 60:
		c.Summary = "Receivers are starting to distrust this brand's mail."
	case worst < 80:
		c.Summary = "Some signals need watching."
	default:
		c.Summary = "Not on any blocklist, receivers trust the mail."
	}
	return c
}

// band scores a rate against the brake levels (watch, slowed, paused).
func band(rate, watch, slowed, paused float64) (int, string) {
	switch {
	case rate >= paused:
		return 0, Failing
	case rate >= slowed:
		return 40, Failing
	case rate >= watch:
		return 70, Warning
	}
	return 100, OK
}

func joinMax(s []string, n int) string {
	out := ""
	for i, x := range s {
		if i == n {
			return out + fmt.Sprintf(" and %d more", len(s)-n)
		}
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}

func mailable(st store.ContactStats) int {
	n := 0
	for _, v := range st.Tiers {
		n += v
	}
	return n
}

func listQuality(in Inputs) Component {
	c := Component{Key: "list_quality", Label: "List quality", Weight: 0.20}
	st := in.Contacts
	held := st.Total - st.Unsubscribed
	var parts []float64
	if held > 0 {
		bad := float64(st.Quarantined+st.SuppressedHere) / float64(held)
		// 25% quarantined or suppressed = 50; half = 0.
		parts = append(parts, 100-bad*200)
		s := OK
		if bad >= 0.1 {
			s = Warning
		}
		if bad >= 0.3 {
			s = Failing
		}
		c.Checks = append(c.Checks, Check{Label: "Quarantined or suppressed", Status: s,
			Detail: fmt.Sprintf("%s of %d contacts (%d quarantined, %d suppressed)", pct(bad), held, st.Quarantined, st.SuppressedHere)})
	}
	if r := in.ImportInvalidRate; r != nil {
		// 2% invalid = 80; 10% = 0.
		parts = append(parts, 100-*r*1000)
		s := OK
		if *r >= 0.02 {
			s = Warning
		}
		if *r >= 0.05 {
			s = Failing
		}
		c.Checks = append(c.Checks, Check{Label: "Invalid addresses in imports (90 days)", Status: s, Detail: pct(*r)})
	}
	if len(parts) == 0 {
		c.Summary = "No contacts yet."
		return c
	}
	var sum float64
	for _, p := range parts {
		sum += p
	}
	c.Score = ptr(clamp(sum / float64(len(parts))))
	switch {
	case *c.Score >= 80:
		c.Summary = "Clean list: few bad or unconfirmed addresses."
	case held > 0 && float64(st.Quarantined)/float64(held) >= 0.1:
		c.Summary = fmt.Sprintf("%d contacts are quarantined; re-confirm or remove them.", st.Quarantined)
	default:
		c.Summary = "Too many invalid or suppressed addresses."
	}
	return c
}

func engagement(in Inputs) Component {
	c := Component{Key: "engagement", Label: "Engagement", Weight: 0.20}
	t := in.Contacts.Tiers
	n := mailable(in.Contacts)
	if n == 0 {
		c.Summary = "No one to email yet."
		return c
	}
	share := func(k store.Tier) float64 { return float64(t[k]) / float64(n) }
	score := share(store.TierActive)*100 + share(store.TierWarm)*75 + share(store.TierAtRisk)*30
	c.Checks = append(c.Checks, Check{
		Label: "Active or warm", Status: OK,
		Detail: fmt.Sprintf("%s of %d mailable contacts", pct(share(store.TierActive)+share(store.TierWarm)), n),
	})
	if share(store.TierActive)+share(store.TierWarm) < 0.6 {
		c.Checks[0].Status = Warning
	}
	if sunset := share(store.TierSunset); sunset > 0 {
		s := OK
		if sunset >= 0.2 {
			s = Warning
		}
		c.Checks = append(c.Checks, Check{Label: "Sunset (no sign of life in 6 months)", Status: s, Detail: pct(sunset) + " — never emailed"})
	}
	unsub := float64(in.Unsubscribes30d) / float64(n+in.Unsubscribes30d)
	// Each 1% unsubscribed in 30 days costs 10 points (max 30).
	score -= math.Min(30, unsub*1000)
	us := OK
	if unsub >= 0.01 {
		us = Warning
	}
	c.Checks = append(c.Checks, Check{Label: "Unsubscribes (30 days)", Status: us, Detail: fmt.Sprintf("%d (%s)", in.Unsubscribes30d, pct(unsub))})
	c.Score = ptr(clamp(score))
	switch {
	case *c.Score >= 80:
		c.Summary = "Most people still open and click."
	case share(store.TierSunset)+share(store.TierAtRisk) >= 0.3:
		c.Summary = "Many contacts haven't engaged in months; send them less."
	default:
		c.Summary = "Engagement is slipping."
	}
	return c
}

// Package brakes stops sending when the feedback says it's hurting (PLAN
// §6.4). Three levels:
//
//	campaign  bounces ≥ 2% · complaints ≥ 0.08% (min 2) · unsubscribes ≥ 1%  → pause the campaign
//	brand     7-day rates: watch → slowed → paused (bounces 1/2/3%, complaints 0.05/0.07/0.08%,
//	          or Gmail spam ≥ 0.2%)                                            → S8 recovery
//	account   complaints across all brands ≥ 0.08%, or Amazon's own brake     → stop everything
//
// The rules here are pure functions over counts; Service applies them.
// Brakes tighten on their own; a paused brand only resumes through the S8
// recovery flow with a second admin.
package brakes

import (
	"fmt"
	"strings"
)

// Counts of what happened to a set of emails.
type Counts struct {
	Delivered    int `json:"delivered"`
	Bounces      int `json:"bounces"` // hard bounces
	Complaints   int `json:"complaints"`
	Unsubscribes int `json:"unsubscribes"`
}

// Attempts: emails that reached a receiving server (delivered or bounced).
func (c Counts) Attempts() int { return c.Delivered + c.Bounces }

// BounceRate is bounces / attempts; ComplaintRate and UnsubRate are per delivered.
func (c Counts) BounceRate() float64    { return ratio(c.Bounces, c.Attempts()) }
func (c Counts) ComplaintRate() float64 { return ratio(c.Complaints, c.Delivered) }
func (c Counts) UnsubRate() float64     { return ratio(c.Unsubscribes, c.Delivered) }

func ratio(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

// atLeast: is rate (a fraction) at or over limit (a percent)? With a little
// slack for floating point: 7/10000 is 0.0699999… percent, and it's 0.07%.
func atLeast(rate, limitPct float64) bool { return rate*100 >= limitPct-1e-9 }

func pct(f float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", f*100), "0"), ".") + "%"
}

// Minimum sample sizes: below them a rate is noise (1 bounce in 20 is 5%).
const (
	CampaignMinAttempts = 100
	BrandMinAttempts    = 200
	AccountMinDelivered = 1000
	MinComplaints       = 2 // "complaints ≥ 0.08% (min 2)"
)

// CampaignLimits are percents from the safety limits (2 = 2%).
type CampaignLimits struct {
	BouncePct, ComplaintPct, UnsubPct float64
}

// Campaign: should the campaign pause? reasons explain why (empty = no).
func Campaign(c Counts, l CampaignLimits) (pause bool, reasons []string) {
	if c.Attempts() >= CampaignMinAttempts && atLeast(c.BounceRate(), l.BouncePct) {
		reasons = append(reasons, fmt.Sprintf("bounces %s ≥ %s", pct(c.BounceRate()), pct(l.BouncePct/100)))
	}
	if c.Complaints >= MinComplaints && atLeast(c.ComplaintRate(), l.ComplaintPct) {
		reasons = append(reasons, fmt.Sprintf("complaints %s ≥ %s (%d)", pct(c.ComplaintRate()), pct(l.ComplaintPct/100), c.Complaints))
	}
	if c.Attempts() >= CampaignMinAttempts && atLeast(c.UnsubRate(), l.UnsubPct) {
		reasons = append(reasons, fmt.Sprintf("unsubscribes %s ≥ %s", pct(c.UnsubRate()), pct(l.UnsubPct/100)))
	}
	return len(reasons) > 0, reasons
}

// State of a brand's brake.
type State string

const (
	Healthy State = "healthy"
	Watch   State = "watch"  // alert; inactive audiences blocked
	Slowed  State = "slowed" // half the send rate; every send needs approval
	Paused  State = "paused" // all marketing stopped → S8 recovery
)

var rank = map[State]int{Healthy: 0, Watch: 1, Slowed: 2, Paused: 3}

// Worse reports whether a is a stricter state than b.
func Worse(a, b State) bool { return rank[a] > rank[b] }

// BrandLimits: the pause line comes from the safety limits; watch and slowed
// sit below it (§6.4 table).
type BrandLimits struct {
	PauseBouncePct    float64 // brand_pause_bounce_pct_7d (3)
	PauseComplaintPct float64 // 0.08
	GmailSpamPct      float64 // brand_pause_gmail_spam_pct (0.2)
}

var (
	watchBouncePct, slowedBouncePct       = 1.0, 2.0
	watchComplaintPct, slowedComplaintPct = 0.05, 0.07
)

// Brand: the state the last 7 days call for. gmailSpam is Postmaster's
// spam rate (nil = unknown).
func Brand(c Counts, gmailSpam *float64, l BrandLimits) (State, []string) {
	state, reasons := Healthy, []string(nil)
	raise := func(s State, why string) {
		if Worse(s, state) {
			state = s
		}
		reasons = append(reasons, why)
	}
	if c.Attempts() >= BrandMinAttempts {
		b := c.BounceRate()
		switch {
		case atLeast(b, l.PauseBouncePct):
			raise(Paused, fmt.Sprintf("7-day bounces %s ≥ %s", pct(b), pct(l.PauseBouncePct/100)))
		case atLeast(b, slowedBouncePct):
			raise(Slowed, fmt.Sprintf("7-day bounces %s ≥ %s", pct(b), pct(slowedBouncePct/100)))
		case atLeast(b, watchBouncePct):
			raise(Watch, fmt.Sprintf("7-day bounces %s ≥ %s", pct(b), pct(watchBouncePct/100)))
		}
	}
	if c.Complaints >= MinComplaints && c.Delivered >= BrandMinAttempts {
		r := c.ComplaintRate()
		switch {
		case atLeast(r, l.PauseComplaintPct):
			raise(Paused, fmt.Sprintf("7-day complaints %s ≥ %s", pct(r), pct(l.PauseComplaintPct/100)))
		case atLeast(r, slowedComplaintPct):
			raise(Slowed, fmt.Sprintf("7-day complaints %s ≥ %s", pct(r), pct(slowedComplaintPct/100)))
		case atLeast(r, watchComplaintPct):
			raise(Watch, fmt.Sprintf("7-day complaints %s ≥ %s", pct(r), pct(watchComplaintPct/100)))
		}
	}
	if gmailSpam != nil && atLeast(*gmailSpam, l.GmailSpamPct) {
		raise(Paused, fmt.Sprintf("Gmail spam rate %s ≥ %s", pct(*gmailSpam), pct(l.GmailSpamPct/100)))
	}
	return state, reasons
}

// Account: should all sending stop? enforcement is Amazon's status for the
// account (HEALTHY, PROBATION, SHUTDOWN; "" = unknown).
func Account(c Counts, complaintPct float64, sendingEnabled bool, enforcement string) (stop bool, reasons []string) {
	if c.Delivered >= AccountMinDelivered && c.Complaints >= MinComplaints && atLeast(c.ComplaintRate(), complaintPct) {
		reasons = append(reasons, fmt.Sprintf("complaints across all brands %s ≥ %s", pct(c.ComplaintRate()), pct(complaintPct/100)))
	}
	if !sendingEnabled {
		reasons = append(reasons, "Amazon SES turned sending off for the account")
	}
	if enforcement == "SHUTDOWN" {
		reasons = append(reasons, "Amazon SES shut the account down")
	}
	return len(reasons) > 0, reasons
}

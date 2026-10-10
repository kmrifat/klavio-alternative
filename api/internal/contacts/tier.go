package contacts

import "time"

// Tier is how recently a contact showed real interest. It decides who a
// campaign may reach: sunset contacts are never sent to, at_risk only when a
// campaign opts in.
type Tier string

const (
	TierActive Tier = "active"  // human click in the last 30 days
	TierWarm   Tier = "warm"    // 31–90 days
	TierAtRisk Tier = "at_risk" // 91–180 days
	TierSunset Tier = "sunset"  // 181+ days, or no signal at all
)

var Tiers = []Tier{TierActive, TierWarm, TierAtRisk, TierSunset}

const day = 24 * time.Hour

// Signals are the facts a tier is computed from.
type Signals struct {
	// LastClickAt is the last human (not bot-filtered) click. Opens are never
	// used: Apple Mail Privacy Protection makes them meaningless.
	LastClickAt *time.Time
	// EngagedAt is engagement we learned about from elsewhere and count like
	// a click: an import rated 4–5 stars by the old provider gets the import
	// time here.
	EngagedAt *time.Time
	// LastOptInAt is the last confirmed signup or preference change. A new
	// subscriber who has not clicked yet is judged from here.
	LastOptInAt *time.Time
	// TrackingAllowed is false for contacts (EU) who did not consent to click
	// tracking. We hold no clicks for them, so they can never be Active and
	// are judged from LastOptInAt alone: Warm for 90 days, then they age out
	// like everyone else. (PLAN.md §9 decision 5, suggested answer.)
	TrackingAllowed bool
}

// TierAt returns the contact's tier at time now. The SQL function
// engagement_tier in the contacts migration must give the same answer.
func TierAt(s Signals, now time.Time) Tier {
	last := s.LastOptInAt
	if s.TrackingAllowed {
		for _, t := range []*time.Time{s.LastClickAt, s.EngagedAt} {
			if t != nil && (last == nil || t.After(*last)) {
				last = t
			}
		}
	}
	if last == nil {
		return TierSunset
	}
	age := now.Sub(*last)
	switch {
	case age <= 30*day && s.TrackingAllowed:
		return TierActive
	case age <= 90*day:
		return TierWarm
	case age <= 180*day:
		return TierAtRisk
	default:
		return TierSunset
	}
}

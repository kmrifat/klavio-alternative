package contacts

import (
	"testing"
	"time"
)

func TestTierAt(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	ago := func(days int) *time.Time {
		t := now.Add(-time.Duration(days) * day)
		return &t
	}
	cases := []struct {
		name string
		s    Signals
		want Tier
	}{
		{"no signal at all", Signals{TrackingAllowed: true}, TierSunset},
		{"clicked today", Signals{LastClickAt: ago(0), TrackingAllowed: true}, TierActive},
		{"clicked 30 days ago", Signals{LastClickAt: ago(30), TrackingAllowed: true}, TierActive},
		{"clicked 31 days ago", Signals{LastClickAt: ago(31), TrackingAllowed: true}, TierWarm},
		{"clicked 90 days ago", Signals{LastClickAt: ago(90), TrackingAllowed: true}, TierWarm},
		{"clicked 91 days ago", Signals{LastClickAt: ago(91), TrackingAllowed: true}, TierAtRisk},
		{"clicked 180 days ago", Signals{LastClickAt: ago(180), TrackingAllowed: true}, TierAtRisk},
		{"clicked 181 days ago", Signals{LastClickAt: ago(181), TrackingAllowed: true}, TierSunset},
		{"new signup, no click yet", Signals{LastOptInAt: ago(3), TrackingAllowed: true}, TierActive},
		{"old click, recent re-opt-in", Signals{LastClickAt: ago(200), LastOptInAt: ago(40), TrackingAllowed: true}, TierWarm},
		{"recent click, old opt-in", Signals{LastClickAt: ago(5), LastOptInAt: ago(400), TrackingAllowed: true}, TierActive},

		{"4-5 stars at import, old opt-in", Signals{EngagedAt: ago(0), LastOptInAt: ago(2000), TrackingAllowed: true}, TierActive},
		{"4-5 stars ages like a click", Signals{EngagedAt: ago(45), LastOptInAt: ago(2000), TrackingAllowed: true}, TierWarm},
		{"a later click beats the import rating", Signals{EngagedAt: ago(100), LastClickAt: ago(10), TrackingAllowed: true}, TierActive},
		{"no tracking: rating is ignored like a click", Signals{EngagedAt: ago(0), LastOptInAt: ago(200)}, TierSunset},

		{"no tracking: fresh signup is only warm", Signals{LastOptInAt: ago(1)}, TierWarm},
		{"no tracking: 90 days", Signals{LastOptInAt: ago(90)}, TierWarm},
		{"no tracking: 120 days", Signals{LastOptInAt: ago(120)}, TierAtRisk},
		{"no tracking: 200 days", Signals{LastOptInAt: ago(200)}, TierSunset},
		{"no tracking: stray click is ignored", Signals{LastClickAt: ago(1), LastOptInAt: ago(200)}, TierSunset},
		{"no tracking, no signal", Signals{}, TierSunset},
	}
	for _, c := range cases {
		if got := TierAt(c.s, now); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

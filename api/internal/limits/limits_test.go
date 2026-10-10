package limits

import "testing"

func TestCompareAndHard(t *testing.T) {
	bounce, _ := Find(CampaignBouncePct) // higher = looser, hard 3
	gap, _ := Find(SameBrandGapHours)    // lower = looser, hard 12
	cases := []struct {
		d        Def
		from, to float64
		want     Direction
	}{
		{bounce, 2, 1.5, Tighter}, {bounce, 2, 2.5, Looser}, {bounce, 2, 2, Same},
		{gap, 16, 20, Tighter}, {gap, 16, 12, Looser},
	}
	for _, c := range cases {
		if got := c.d.Compare(c.from, c.to); got != c.want {
			t.Errorf("%s %v→%v = %v, want %v", c.d.Key, c.from, c.to, got, c.want)
		}
	}
	if !bounce.WithinHard(3) || bounce.WithinHard(3.01) || !gap.WithinHard(12) || gap.WithinHard(11) {
		t.Error("hard limits")
	}
	for _, d := range Defs {
		if !d.WithinHard(d.Value) {
			t.Errorf("%s: default %v is past its own hard limit %v", d.Key, d.Value, d.Hard)
		}
	}
}

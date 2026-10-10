package sending

import (
	"context"
	"testing"
	"time"
)

func TestSendAfter(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		name, now, tz, want string
	}{
		{"daytime goes now", "2026-10-10T12:00:00Z", "Europe/Dublin", "2026-10-10T12:00:00Z"},
		{"21:00 local waits for 08:00 next day", "2026-10-10T20:00:00Z", "Europe/Dublin", "2026-10-11T07:00:00Z"}, // Dublin is UTC+1 in October
		{"02:00 local waits for 08:00 same day", "2026-10-10T01:00:00Z", "Europe/Dublin", "2026-10-10T07:00:00Z"},
		{"07:59 local still quiet", "2026-10-10T06:59:00Z", "Europe/Dublin", "2026-10-10T07:00:00Z"},
		{"08:00 local is fine", "2026-10-10T07:00:00Z", "Europe/Dublin", "2026-10-10T07:00:00Z"},
		{"20:59 local is fine", "2026-10-10T19:59:00Z", "Europe/Dublin", "2026-10-10T19:59:00Z"},
		{"far timezone", "2026-10-10T12:00:00Z", "Asia/Tokyo", "2026-10-10T23:00:00Z"}, // 21:00 Tokyo → 08:00 next day
		{"unknown timezone goes now", "2026-10-10T02:00:00Z", "", "2026-10-10T02:00:00Z"},
		{"bad timezone goes now", "2026-10-10T02:00:00Z", "Mars/Olympus", "2026-10-10T02:00:00Z"},
		{"DST change night", "2026-03-29T00:30:00Z", "Europe/Dublin", "2026-03-29T07:00:00Z"}, // clocks go forward at 01:00
	}
	for _, c := range cases {
		if got := SendAfter(at(c.now), c.tz); !got.Equal(at(c.want)) {
			t.Errorf("%s: got %s, want %s", c.name, got.UTC().Format(time.RFC3339), c.want)
		}
	}
}

func TestPacer(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var slept []time.Duration
	p := NewPacer(10)
	p.now = func() time.Time { return now }
	p.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }

	for range 3 {
		_ = p.Wait(context.Background())
	}
	want := []time.Duration{0, 100 * time.Millisecond, 200 * time.Millisecond}
	for i, w := range want {
		if slept[i] != w {
			t.Fatalf("waits %v, want %v", slept, want)
		}
	}
	p.Slow(2 * time.Second)
	_ = p.Wait(context.Background())
	if got := slept[3]; got != 2*time.Second {
		t.Fatalf("after Slow: waited %v", got)
	}
	// A wait for a later slot gives up when the context is cancelled.
	slow := NewPacer(0.001)
	_ = slow.Wait(context.Background()) // first slot is immediate
	if err := slow.Wait(cancelled()); err == nil {
		t.Fatal("Wait ignored a cancelled context")
	}
}

func cancelled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

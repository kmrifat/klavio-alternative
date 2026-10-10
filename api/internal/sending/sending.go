// Package sending holds the pieces of the sending worker that don't touch
// the database: the send-rate pacer and quiet hours.
package sending

import (
	"context"
	"sync"
	"time"
)

// Pacer spaces sends evenly at a fixed rate (SES's max send rate). One per
// process; v1 runs a single worker replica, so it is the whole account's rate.
type Pacer struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
}

// NewPacer allows perSecond sends a second (at least one).
func NewPacer(perSecond float64) *Pacer {
	if perSecond <= 0 {
		perSecond = 1
	}
	return &Pacer{interval: time.Duration(float64(time.Second) / perSecond), now: time.Now, sleep: sleepCtx}
}

// Wait blocks until the next send slot.
func (p *Pacer) Wait(ctx context.Context) error {
	p.mu.Lock()
	now := p.now()
	at := p.next
	if at.Before(now) {
		at = now
	}
	p.next = at.Add(p.interval)
	p.mu.Unlock()
	return p.sleep(ctx, at.Sub(now))
}

// Slow pushes every later slot back by d, after SES says we're too fast.
func (p *Pacer) Slow(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if at := p.now().Add(d); at.After(p.next) {
		p.next = at
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Quiet hours, recipient's local time: nothing lands from 21:00 to 08:00.
const (
	QuietFrom = 21
	QuietTo   = 8
)

var locations sync.Map // name → *time.Location (nil if unknown)

func location(name string) *time.Location {
	if v, ok := locations.Load(name); ok {
		loc, _ := v.(*time.Location)
		return loc
	}
	loc, err := time.LoadLocation(name)
	if err != nil || name == "" || name == "Local" {
		loc = nil
	}
	locations.Store(name, loc)
	return loc
}

// SendAfter is the earliest moment from now an email may reach someone in
// timezone tz without landing in their quiet hours. With no known
// timezone it is now: we can't tell their night from their day.
func SendAfter(now time.Time, tz string) time.Time {
	loc := location(tz)
	if loc == nil {
		return now
	}
	local := now.In(loc)
	h := local.Hour()
	if h >= QuietTo && h < QuietFrom {
		return now
	}
	morning := time.Date(local.Year(), local.Month(), local.Day(), QuietTo, 0, 0, 0, loc)
	if h >= QuietFrom {
		morning = morning.AddDate(0, 0, 1)
	}
	return morning.UTC()
}

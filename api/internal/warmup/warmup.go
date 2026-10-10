// Package warmup paces a new brand's sending: mailbox providers trust a new
// domain gradually, so its daily volume rises over ~21 days (S1 "Day 9 of
// ~21", S8 "cap 3,200/day"). The schedule starts the first day the brand
// actually sends.
//
// The sending worker uses it like this, inside the transaction that queues
// a batch:
//
//	granted, err := warm.Reserve(ctx, tx, brandID, today, len(batch))
//	// send `granted` now; the rest waits for tomorrow
//
// While a brand is warming up, every send also needs a second admin
// (approvals.KindWarmupSend; see InWarmup).
package warmup

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// Schedule[i] is the cap on day i+1. Roughly ×1.4 a day, from 200 to 110,000.
var Schedule = []int{
	200, 300, 400, 600, 800, 1100, 1600, 2200, 3200, 4500, 6000,
	8000, 11000, 15000, 20000, 27000, 36000, 48000, 65000, 85000, 110000,
}

// Days is the length of the warm-up.
var Days = len(Schedule)

// ErrNeedsTx: Reserve must run inside the transaction that queues the batch.
var ErrNeedsTx = errors.New("warmup: Reserve needs a transaction (store.InTx)")

// Unlimited is what CapFor returns once the warm-up is over.
const Unlimited = math.MaxInt

type Service struct{ store *store.Store }

func NewService(st *store.Store) *Service { return &Service{store: st} }

// utcDay truncates to the UTC calendar day.
func utcDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Status of a brand's warm-up on a given day.
type Status struct {
	Day       int        `json:"day"`        // 1-based; Days+1 or more = complete
	Days      int        `json:"days"`       // length of the warm-up
	Cap       int        `json:"cap"`        // today's cap (Unlimited when complete)
	Sent      int        `json:"sent"`       // sent today
	Complete  bool       `json:"complete"`   // warm-up is over
	StartedOn *time.Time `json:"started_on"` // nil = hasn't sent yet (day 1 when it does)
}

func dayNumber(started *time.Time, day time.Time) int {
	if started == nil {
		return 1
	}
	return int(utcDay(day).Sub(utcDay(*started)).Hours()/24) + 1
}

func capForDay(n int) int {
	if n < 1 {
		n = 1
	}
	if n > Days {
		return Unlimited
	}
	return Schedule[n-1]
}

// StatusOn reports the brand's warm-up on a day.
func (s *Service) StatusOn(ctx context.Context, tx *store.Store, brandID uuid.UUID, day time.Time) (Status, error) {
	if tx == nil {
		tx = s.store
	}
	started, err := tx.WarmupStart(ctx, brandID)
	if err != nil {
		return Status{}, err
	}
	sent, err := tx.SentOn(ctx, brandID, utcDay(day))
	if err != nil {
		return Status{}, err
	}
	n := dayNumber(started, day)
	c := capForDay(n)
	return Status{Day: n, Days: Days, Cap: c, Sent: sent, Complete: c == Unlimited, StartedOn: started}, nil
}

// CapFor returns how many more emails the brand may send on the day:
// Unlimited once the warm-up is complete. Read-only; use Reserve to claim.
func (s *Service) CapFor(ctx context.Context, brandID uuid.UUID, day time.Time) (remaining int, err error) {
	st, err := s.StatusOn(ctx, nil, brandID, day)
	if err != nil {
		return 0, err
	}
	if st.Complete {
		return Unlimited, nil
	}
	return max(st.Cap-st.Sent, 0), nil
}

// InWarmup: is the brand still warming up on the day? (Sends then need a
// second admin: approvals.KindWarmupSend.)
func (s *Service) InWarmup(ctx context.Context, brandID uuid.UUID, day time.Time) (bool, error) {
	st, err := s.StatusOn(ctx, nil, brandID, day)
	return !st.Complete, err
}

// Reserve atomically claims up to `want` sends for the brand on the day and
// records them, returning how many were granted (0 when today's cap is used
// up). Call it inside the transaction that queues the batch; the first
// reservation starts the warm-up.
func (s *Service) Reserve(ctx context.Context, tx *store.Store, brandID uuid.UUID, day time.Time, want int) (granted int, err error) {
	if !tx.InTransaction() {
		// The advisory lock lasts only as long as the transaction.
		return 0, ErrNeedsTx
	}
	if want <= 0 {
		return 0, nil
	}
	d := utcDay(day)
	if err := tx.Lock(ctx, "warmup:"+brandID.String()+":"+d.Format("2006-01-02")); err != nil {
		return 0, err
	}
	st, err := s.StatusOn(ctx, tx, brandID, d)
	if err != nil {
		return 0, err
	}
	granted = want
	if !st.Complete {
		granted = min(want, max(st.Cap-st.Sent, 0))
	}
	if granted > 0 {
		if _, err := tx.RecordSends(ctx, brandID, d, granted); err != nil {
			return 0, err
		}
	}
	return granted, nil
}

// Release gives back n sends reserved for the day that didn't go out. If
// that leaves the brand with no sends at all, the warm-up hasn't started.
func (s *Service) Release(ctx context.Context, tx *store.Store, brandID uuid.UUID, day time.Time, n int) error {
	if n <= 0 {
		return nil
	}
	return tx.ReleaseSends(ctx, brandID, utcDay(day), n)
}

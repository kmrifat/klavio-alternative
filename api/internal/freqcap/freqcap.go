// Package freqcap is the cross-brand frequency cap: one person gets at most
// a few marketing emails a day and a week from all brands together, and some
// hours between two emails from the same brand. A "person" is an email
// address: the same address at two brands is the same inbox.
//
// The limits are safety limits (S17): cross_brand_cap_per_day (2),
// cross_brand_cap_per_week (6) and same_brand_min_gap_hours (16). Windows
// are rolling: the last 24 hours, the last 7 days.
//
// The sending worker calls Reserve inside the transaction that queues a
// batch; it checks and records atomically, so two brands sending at the same
// moment can't both squeeze in:
//
//	allowed, excluded, err := caps.Reserve(ctx, tx, brandID, contactIDs, now)
//	// send to `allowed`; store each excluded reason on the audience row
//
// Filter is the read-only version, for showing exclusions before a send.
package freqcap

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/limits"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// Reason a contact was held back.
type Reason string

const (
	ReasonDay          Reason = "freq_cap_day"    // had the daily maximum from all brands
	ReasonWeek         Reason = "freq_cap_week"   // had the weekly maximum from all brands
	ReasonSameBrandGap Reason = "same_brand_gap"  // this brand emailed them too recently
	ReasonUnknown      Reason = "unknown_contact" // not a contact of this brand
)

type Excluded struct {
	ContactID uuid.UUID `json:"contact_id"`
	Reason    Reason    `json:"reason"`
}

// ErrNeedsTx: Reserve must run inside the transaction that queues the batch.
var ErrNeedsTx = errors.New("freqcap: Reserve needs a transaction (store.InTx)")

// Keep is how long sends are remembered (the longest window plus a day).
const Keep = 8 * 24 * time.Hour

type Service struct {
	store *store.Store
	limit func(ctx context.Context, key string) float64
}

// NewService: limit reads a safety limit (limits.Service.Get).
func NewService(st *store.Store, limit func(ctx context.Context, key string) float64) *Service {
	return &Service{store: st, limit: limit}
}

// Limits in force now.
type Limits struct {
	PerDay       int           `json:"per_day"`
	PerWeek      int           `json:"per_week"`
	SameBrandGap time.Duration `json:"same_brand_gap"`
}

func (s *Service) Limits(ctx context.Context) Limits {
	return Limits{
		PerDay:       int(s.limit(ctx, limits.CapPerDay)),
		PerWeek:      int(s.limit(ctx, limits.CapPerWeek)),
		SameBrandGap: time.Duration(s.limit(ctx, limits.SameBrandGapHours) * float64(time.Hour)),
	}
}

// Filter splits contacts into those who may get a marketing email from the
// brand at `now` and those held back, with the reason. Read-only. Order of
// `allowed` follows contactIDs.
func (s *Service) Filter(ctx context.Context, tx *store.Store, brandID uuid.UUID, contactIDs []uuid.UUID, now time.Time) (allowed []uuid.UUID, excluded []Excluded, err error) {
	if tx == nil {
		tx = s.store
	}
	l := s.Limits(ctx)
	hist, err := tx.SendHistory(ctx, brandID, contactIDs, now)
	if err != nil {
		return nil, nil, err
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range contactIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		h, ok := hist[id]
		switch {
		case !ok:
			excluded = append(excluded, Excluded{id, ReasonUnknown})
		case h.Last24h >= l.PerDay:
			excluded = append(excluded, Excluded{id, ReasonDay})
		case h.Last7d >= l.PerWeek:
			excluded = append(excluded, Excluded{id, ReasonWeek})
		case h.LastSameBrand != nil && now.Sub(*h.LastSameBrand) < l.SameBrandGap:
			excluded = append(excluded, Excluded{id, ReasonSameBrandGap})
		default:
			allowed = append(allowed, id)
		}
	}
	return allowed, excluded, nil
}

// Reserve is Filter plus recording a send at `now` for everyone allowed,
// atomically: each address is locked for the rest of the transaction.
func (s *Service) Reserve(ctx context.Context, tx *store.Store, brandID uuid.UUID, contactIDs []uuid.UUID, now time.Time) (allowed []uuid.UUID, excluded []Excluded, err error) {
	if !tx.InTransaction() {
		return nil, nil, ErrNeedsTx
	}
	if err := tx.LockSendHistory(ctx, brandID, contactIDs); err != nil {
		return nil, nil, err
	}
	if allowed, excluded, err = s.Filter(ctx, tx, brandID, contactIDs, now); err != nil {
		return nil, nil, err
	}
	if len(allowed) > 0 {
		if err := tx.RecordMarketingSends(ctx, brandID, allowed, now); err != nil {
			return nil, nil, err
		}
	}
	return allowed, excluded, nil
}

// Purge forgets sends older than Keep (hourly job).
func (s *Service) Purge(ctx context.Context) (int64, error) {
	return s.store.PurgeMarketingSends(ctx, time.Now().Add(-Keep))
}

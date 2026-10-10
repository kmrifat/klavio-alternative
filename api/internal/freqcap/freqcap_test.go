package freqcap

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/limits"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/testdb"
)

func defaults(_ context.Context, key string) float64 {
	d, _ := limits.Find(key)
	return d.Value
}

type fixture struct {
	st     *store.Store
	caps   *Service
	brands []uuid.UUID
	// contact[i] is ann@example.com at brand i.
	contact []uuid.UUID
}

func setup(t *testing.T, nBrands int) fixture {
	t.Helper()
	ctx := context.Background()
	st := store.New(testdb.New(t))
	f := fixture{st: st, caps: NewService(st, defaults)}
	for i := range nBrands {
		slug := fmt.Sprintf("brand%d", i)
		b, err := st.CreateBrand(ctx, store.Brand{Slug: slug, Name: slug, Color: "#000000", PostalAddress: "1 Main St",
			SendingDomain: "news." + slug + ".test", MailFromDomain: "bounce.news." + slug + ".test", LinkDomain: "go." + slug + ".test",
			SESConfigurationSet: slug, SESIdentityStatus: "SUCCESS", Status: store.BrandReady})
		if err != nil {
			t.Fatal(err)
		}
		c, err := st.InsertContact(ctx, store.NewContact{BrandID: b.ID, Email: "Ann@Example.com", Status: store.ContactSubscribed, ConsentBasis: "double_opt_in"})
		if err != nil {
			t.Fatal(err)
		}
		f.brands, f.contact = append(f.brands, b.ID), append(f.contact, c)
	}
	return f
}

// reserve one contact for brand i at `at`; returns the reason ("" = allowed).
func (f fixture) reserve(t *testing.T, i int, at time.Time) Reason {
	t.Helper()
	var reason Reason
	err := f.st.InTx(context.Background(), func(tx *store.Store) error {
		allowed, excluded, err := f.caps.Reserve(context.Background(), tx, f.brands[i], []uuid.UUID{f.contact[i]}, at)
		if len(allowed) == 0 && len(excluded) == 1 {
			reason = excluded[0].Reason
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return reason
}

func TestCrossBrandCap(t *testing.T) {
	f := setup(t, 3)
	t0 := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)

	if r := f.reserve(t, 0, t0); r != "" {
		t.Fatalf("first email: %s", r)
	}
	if r := f.reserve(t, 0, t0.Add(time.Hour)); r != ReasonSameBrandGap {
		t.Fatalf("same brand an hour later: %q", r)
	}
	if r := f.reserve(t, 1, t0.Add(time.Hour)); r != "" {
		t.Fatalf("another brand: %q", r)
	}
	// Two in 24 hours from any brands: the third brand waits.
	if r := f.reserve(t, 2, t0.Add(2*time.Hour)); r != ReasonDay {
		t.Fatalf("third email in a day: %q", r)
	}
	if r := f.reserve(t, 2, t0.Add(25*time.Hour)); r != "" {
		t.Fatalf("next day: %q", r)
	}
	// 16 hours is the same-brand gap.
	if r := f.reserve(t, 0, t0.Add(15*time.Hour)); r != ReasonDay {
		t.Fatalf("day cap is checked first: %q", r)
	}

	// Weekly: 3 so far; three more days of one email each = 6.
	for d := 2; d <= 4; d++ {
		if r := f.reserve(t, d%3, t0.Add(time.Duration(d)*24*time.Hour+time.Hour)); r != "" {
			t.Fatalf("day %d: %q", d, r)
		}
	}
	if r := f.reserve(t, 1, t0.Add(5*24*time.Hour+2*time.Hour)); r != ReasonWeek {
		t.Fatalf("seventh in a week: %q", r)
	}
	// Filter is read-only and agrees.
	_, ex, err := f.caps.Filter(context.Background(), nil, f.brands[1], []uuid.UUID{f.contact[1], uuid.New()}, t0.Add(5*24*time.Hour+2*time.Hour))
	if err != nil || len(ex) != 2 || ex[0].Reason != ReasonWeek || ex[1].Reason != ReasonUnknown {
		t.Fatalf("filter: %v %v", ex, err)
	}
	// Nothing older than 7 days counts.
	if r := f.reserve(t, 1, t0.Add(9*24*time.Hour)); r != "" {
		t.Fatalf("after a week: %q", r)
	}
}

func TestReserveIsAtomicAcrossBrands(t *testing.T) {
	f := setup(t, 6)
	at := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	allowed := 0
	var wg sync.WaitGroup
	for round := range 4 {
		for i := range f.brands {
			wg.Go(func() {
				_ = f.st.InTx(context.Background(), func(tx *store.Store) error {
					a, _, err := f.caps.Reserve(context.Background(), tx, f.brands[i], []uuid.UUID{f.contact[i]}, at.Add(time.Duration(round)*time.Second))
					mu.Lock()
					allowed += len(a)
					mu.Unlock()
					return err
				})
			})
		}
	}
	wg.Wait()
	if allowed != 2 {
		t.Fatalf("%d emails allowed at once across brands, cap is 2 a day", allowed)
	}
}

func TestReserveNeedsATransaction(t *testing.T) {
	f := setup(t, 1)
	if _, _, err := f.caps.Reserve(context.Background(), f.st, f.brands[0], f.contact, time.Now()); !errors.Is(err, ErrNeedsTx) {
		t.Fatalf("outside a transaction: %v", err)
	}
}

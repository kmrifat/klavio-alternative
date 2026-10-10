package warmup

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/testdb"
)

func TestSchedule(t *testing.T) {
	if Days != 21 || capForDay(1) != 200 || capForDay(9) != 3200 || capForDay(22) != Unlimited {
		t.Fatalf("schedule: days=%d day9=%d", Days, capForDay(9))
	}
	for i := 1; i < len(Schedule); i++ {
		if Schedule[i] <= Schedule[i-1] {
			t.Fatalf("schedule must rise: day %d", i+1)
		}
	}
	start := time.Date(2026, 10, 1, 23, 0, 0, 0, time.UTC)
	if n := dayNumber(&start, time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)); n != 9 {
		t.Fatalf("day number = %d", n)
	}
}

func TestReserveIsAtomic(t *testing.T) {
	st := store.New(testdb.New(t))
	ctx := context.Background()
	b, err := st.CreateBrand(ctx, store.Brand{Slug: "brightside", Name: "B", Color: "#000000", PostalAddress: "x",
		SendingDomain: "news.b.co", MailFromDomain: "bounce.news.b.co", LinkDomain: "go.b.co", SESConfigurationSet: "brand-b"})
	if err != nil {
		t.Fatal(err)
	}
	w := NewService(st)
	day1 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	if rem, _ := w.CapFor(ctx, b.ID, day1); rem != 200 {
		t.Fatalf("not started: %d", rem)
	}
	// 10 workers each want 50 on day 1: exactly 200 granted in total.
	var mu sync.Mutex
	total := 0
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = st.InTx(ctx, func(tx *store.Store) error {
				g, err := w.Reserve(ctx, tx, b.ID, day1, 50)
				mu.Lock()
				total += g
				mu.Unlock()
				return err
			})
		}()
	}
	wg.Wait()
	if total != 200 {
		t.Fatalf("granted %d on day 1, cap 200", total)
	}
	if rem, _ := w.CapFor(ctx, b.ID, day1); rem != 0 {
		t.Fatalf("remaining %d", rem)
	}
	// Day 9 of the schedule, and complete after day 21.
	if rem, _ := w.CapFor(ctx, b.ID, day1.AddDate(0, 0, 8)); rem != 3200 {
		t.Fatalf("day 9: %d", rem)
	}
	if ok, _ := w.InWarmup(ctx, b.ID, day1.AddDate(0, 0, 21)); ok {
		t.Fatal("day 22 should be past the warm-up")
	}
	if rem, _ := w.CapFor(ctx, b.ID, day1.AddDate(0, 0, 21)); rem != Unlimited {
		t.Fatalf("after warm-up: %d", rem)
	}
}

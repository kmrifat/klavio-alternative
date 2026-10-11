package contactsapi

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/cache"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/testdb"
)

type countQueue struct{ n int }

func (q *countQueue) QueueConfirmation(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
	q.n++
	return nil
}

// A bot filling a form with strangers' addresses runs into the sign-up IP's
// hourly budget, then the brand's daily one, which tells the admins once.
func TestConfirmationBudgets(t *testing.T) {
	ctx := context.Background()
	st := store.New(testdb.New(t))
	b, err := st.CreateBrand(ctx, store.Brand{Slug: "acme", Name: "Acme", Color: "#000000", PostalAddress: "1 Main St",
		SendingDomain: "news.acme.test", MailFromDomain: "bounce.news.acme.test", LinkDomain: "go.acme.test", SESConfigurationSet: "brand-acme"})
	if err != nil {
		t.Fatal(err)
	}
	q := &countQueue{}
	a := &API{Store: st, Limiter: &cache.MemoryLimiter{}, Queue: q, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	send := func(i int, ip string) string {
		return a.queueConfirmation(ctx, b, uuid.New(), uuid.Nil, uuid.NewString()+"@example.com", ip)
	}

	for i := range IPConfirmEmailsPerHour {
		if got := send(i, "203.0.113.9"); got != "sent" {
			t.Fatalf("sign-up %d from one IP: %s", i, got)
		}
	}
	if got := send(0, "203.0.113.9"); got != "limit_reached" {
		t.Fatalf("past the IP's hourly budget: %s", got)
	}

	for i := q.n; i < BrandConfirmEmailsPerDay; i++ {
		if got := send(i, ""); got != "sent" {
			t.Fatalf("sign-up %d: %s", i, got)
		}
	}
	for range 3 {
		if got := send(0, "198.51.100.7"); got != "limit_reached" {
			t.Fatalf("past the brand's daily budget: %s", got)
		}
	}
	alerts, err := st.ListAlerts(ctx, true, 10)
	if err != nil || len(alerts) != 1 || alerts[0].Kind != "signup_flood" || *alerts[0].BrandID != b.ID {
		t.Fatalf("alerts: %+v %v", alerts, err)
	}
	if q.n != BrandConfirmEmailsPerDay {
		t.Fatalf("%d confirmation emails queued", q.n)
	}
}

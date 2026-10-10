package events

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// SoftBounceLimit soft bounces in SoftBounceWindow suppress an address.
const (
	SoftBounceLimit  = 3
	SoftBounceWindow = 30 * 24 * time.Hour
)

type Processor struct {
	Store *store.Store
	Log   *slog.Logger
}

// Outcome of one event, for logs and tests.
type Outcome struct {
	Duplicate  bool
	Suppressed []string // addresses newly suppressed
}

// Process stores the event and applies its effects, in one transaction.
// Running it again for the same event does nothing.
func (p *Processor) Process(ctx context.Context, e Event) (Outcome, error) {
	var out Outcome
	err := p.Store.InTx(ctx, func(tx *store.Store) error {
		out = Outcome{}
		var brandID *uuid.UUID
		var brand store.Brand
		if e.ConfigurationSet != "" {
			b, err := tx.BrandByConfigurationSet(ctx, e.ConfigurationSet)
			switch {
			case err == nil:
				brand, brandID = b, &b.ID
			case !errors.Is(err, store.ErrNotFound):
				return err
			}
		}
		var sendID *int64
		if e.SendID > 0 {
			sendID = &e.SendID
		}
		row := store.EmailEvent{EventID: e.ID, Type: string(e.Type), Subtype: e.Subtype, BrandID: brandID,
			SESMessageID: e.MessageID, SendID: sendID, OccurredAt: e.OccurredAt, Detail: e.Detail}

		recipients := e.Recipients
		if !perRecipient(e.Type) {
			// One row for events not about a specific recipient (open, click, send…).
			fresh, err := tx.InsertEmailEvent(ctx, row)
			out.Duplicate = !fresh
			return err
		}
		fresh := false
		for _, r := range recipients {
			row.EmailHash = store.EmailHash(r)
			inserted, err := tx.InsertEmailEvent(ctx, row)
			if err != nil {
				return err
			}
			if !inserted {
				continue // this recipient's part was handled before
			}
			fresh = true
			if err := p.apply(ctx, tx, e, brand, brandID, r, &out); err != nil {
				return err
			}
		}
		out.Duplicate = !fresh
		return nil
	})
	return out, err
}

func perRecipient(t Type) bool {
	switch t {
	case Bounce, Complaint, Delivery, DeliveryDelay:
		return true
	}
	return false
}

// apply: hard bounce or complaint → suppress now; soft bounce → suppress
// on the third in 30 days. Each suppression gets a consent record at the
// brand that sent the email.
func (p *Processor) apply(ctx context.Context, tx *store.Store, e Event, brand store.Brand, brandID *uuid.UUID, email string, out *Outcome) error {
	var reason store.SuppressionReason
	var kind, note string
	switch {
	case e.Type == Complaint:
		reason, kind, note = store.SuppressComplaint, "complained", "spam complaint ("+orUnknown(e.Subtype)+")"
	case e.Permanent():
		reason, kind, note = store.SuppressHardBounce, "bounced", "hard bounce ("+e.Subtype+")"
	case e.SoftCounts():
		n, err := tx.SoftBouncesSince(ctx, store.EmailHash(email), e.OccurredAt.Add(-SoftBounceWindow))
		if err != nil {
			return err
		}
		if n < SoftBounceLimit {
			return nil
		}
		reason, kind, note = store.SuppressSoftBounce, "bounced", "soft bounces: 3 in 30 days (last "+e.Subtype+")"
	default:
		return nil
	}
	if err := tx.Suppress(ctx, email, reason, brandID, note, nil); err != nil {
		return err
	}
	out.Suppressed = append(out.Suppressed, strings.ToLower(email))
	p.Log.InfoContext(ctx, "address suppressed", "reason", reason, "brand", brand.Slug, "ses_message_id", e.MessageID)
	if brandID == nil {
		return nil
	}
	c, err := tx.ContactByEmail(ctx, *brandID, email)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return tx.AddConsentRecord(ctx, store.NewConsentRecord{
		BrandID: *brandID, ContactID: c.ID, Email: c.Email, Kind: kind, Method: "ses",
		Source: note + " · SES message " + e.MessageID, OccurredAt: e.OccurredAt,
	})
}

func orUnknown(s string) string {
	if s == "" {
		return "no feedback type"
	}
	return s
}

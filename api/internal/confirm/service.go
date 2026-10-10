package confirm

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"log/slog"
	"slices"
	texttemplate "text/template"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/freqcap"
	"github.com/kmrifat/klavio-alternative/api/internal/limits"
	"github.com/kmrifat/klavio-alternative/api/internal/mailer"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/unsubscribe"
	"github.com/kmrifat/klavio-alternative/api/internal/warmup"
)

//go:embed emails.html emails.txt
var emailFS embed.FS

var (
	htmlEmails = htmltemplate.Must(htmltemplate.ParseFS(emailFS, "emails.html"))
	textEmails = texttemplate.Must(texttemplate.ParseFS(emailFS, "emails.txt"))
)

// Service sends confirmation and re-permission emails and applies the
// confirmations.
type Service struct {
	store    *store.Store
	signer   *Signer
	unsub    *unsubscribe.Signer
	sender   mailer.Sender
	caps     *freqcap.Service
	warm     *warmup.Service
	limit    func(ctx context.Context, key string) float64
	override string // PUBLIC_BASE_URL_OVERRIDE (development)
	log      *slog.Logger
	now      func() time.Time
}

type Deps struct {
	Store    *store.Store
	Signer   *Signer
	Unsub    *unsubscribe.Signer
	Sender   mailer.Sender
	Caps     *freqcap.Service
	Warmup   *warmup.Service
	Limit    func(ctx context.Context, key string) float64
	Override string
	Log      *slog.Logger
}

func NewService(d Deps) *Service {
	return &Service{store: d.Store, signer: d.Signer, unsub: d.Unsub, sender: d.Sender, caps: d.Caps, warm: d.Warmup,
		limit: d.Limit, override: d.Override, log: d.Log, now: time.Now}
}

// SetSender swaps the sender (the SES sender, once it exists).
func (s *Service) SetSender(m mailer.Sender) { s.sender = m }

type emailData struct {
	Brand      store.Brand
	Contact    store.Contact
	ConfirmURL string
	UnsubURL   string
	Days       int
}

func render(name string, d emailData) (html, text string, err error) {
	var h, t bytes.Buffer
	if err := htmlEmails.ExecuteTemplate(&h, name, d); err != nil {
		return "", "", err
	}
	if err := textEmails.ExecuteTemplate(&t, name, d); err != nil {
		return "", "", err
	}
	return h.String(), t.String(), nil
}

func (s *Service) base(b store.Brand) string { return unsubscribe.BaseURL(b.LinkDomain, s.override) }

// SendConfirmation emails the confirmation link (P2) to someone awaiting
// double opt-in. People who confirmed, left or were suppressed meanwhile get
// nothing. Run by the confirmation job.
func (s *Service) SendConfirmation(ctx context.Context, brandID, contactID uuid.UUID) error {
	b, err := s.store.BrandByID(ctx, brandID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	c, err := s.store.ContactByID(ctx, brandID, contactID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if c.Status != store.ContactPending || c.SuppressionReason != nil {
		return nil
	}
	now := s.now()
	d := emailData{Brand: b, Contact: c, Days: int(TTL.Hours() / 24),
		ConfirmURL: s.signer.URL(s.base(b), Claims{BrandID: b.ID, ContactID: c.ID, Purpose: PurposeOptIn, IssuedAt: now.Truncate(time.Second)})}
	html, text, err := render("confirmation", d)
	if err != nil {
		return err
	}
	err = s.sender.Send(ctx, mailer.Message{Brand: b, To: c.Email, Subject: "Please confirm your subscription to " + b.Name,
		HTML: html, Text: text, Kind: "confirmation"})
	if err != nil {
		return fmt.Errorf("sending confirmation: %w", err)
	}
	return s.store.SetConfirmationSent(ctx, c.ID, now)
}

// SendRepermissions is the hourly job: for each ready brand that turned
// re-permission on, ask quarantined contacts to confirm, up to the daily
// limit (repermission_emails_per_day), within the warm-up cap and the
// cross-brand frequency cap.
func (s *Service) SendRepermissions(ctx context.Context) error {
	all, err := s.store.ListBrands(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, b := range all {
		if !b.RepermissionOn || b.Status != store.BrandReady {
			continue
		}
		if err := s.repermissionBrand(ctx, b); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", b.Slug, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) repermissionBrand(ctx context.Context, b store.Brand) error {
	now := s.now()
	y, m, d := now.UTC().Date()
	sent, err := s.store.RepermissionSentSince(ctx, b.ID, time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
	if err != nil {
		return err
	}
	room := int(s.limit(ctx, limits.RepermissionEmailsPerDay)) - sent
	if room <= 0 {
		return nil
	}
	var batch []store.Contact
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		claimed, err := tx.ClaimRepermission(ctx, b.ID, room, now)
		if err != nil || len(claimed) == 0 {
			return err
		}
		ids := make([]uuid.UUID, len(claimed))
		for i, c := range claimed {
			ids[i] = c.ID
		}
		// Frequency cap first (read-only), so warm-up only counts people
		// who'll really get an email; then claim both.
		ok, _, err := s.caps.Filter(ctx, tx, b.ID, ids, now)
		if err != nil {
			return err
		}
		granted, err := s.warm.Reserve(ctx, tx, b.ID, now, len(ok))
		if err != nil {
			return err
		}
		allowed, _, err := s.caps.Reserve(ctx, tx, b.ID, ok[:granted], now)
		if err != nil {
			return err
		}
		// Whoever can't be emailed today goes back in the queue.
		var back []uuid.UUID
		for _, c := range claimed {
			if slices.Contains(allowed, c.ID) {
				batch = append(batch, c)
			} else {
				back = append(back, c.ID)
			}
		}
		if len(back) > 0 {
			return tx.UnclaimRepermission(ctx, back)
		}
		return nil
	})
	if err != nil {
		return err
	}
	var failed []uuid.UUID
	for _, c := range batch {
		if err := s.sendRepermission(ctx, b, c, now); err != nil {
			s.log.WarnContext(ctx, "re-permission email", "brand", b.Slug, "err", err)
			failed = append(failed, c.ID)
		}
	}
	if len(failed) > 0 {
		return errors.Join(s.store.UnclaimRepermission(ctx, failed), fmt.Errorf("%d re-permission emails failed", len(failed)))
	}
	if len(batch) > 0 {
		s.log.InfoContext(ctx, "re-permission emails sent", "brand", b.Slug, "count", len(batch))
	}
	return nil
}

func (s *Service) sendRepermission(ctx context.Context, b store.Brand, c store.Contact, now time.Time) error {
	base := s.base(b)
	unsub := unsubscribe.Claims{BrandID: b.ID, ContactID: c.ID}
	d := emailData{Brand: b, Contact: c, Days: int(TTL.Hours() / 24),
		ConfirmURL: s.signer.URL(base, Claims{BrandID: b.ID, ContactID: c.ID, Purpose: PurposeRepermission, IssuedAt: now.Truncate(time.Second)}),
		UnsubURL:   s.unsub.PageURL(base, unsub)}
	html, text, err := render("repermission", d)
	if err != nil {
		return err
	}
	lu, lup := unsubscribe.Headers(s.unsub.OneClickURL(base, unsub))
	return s.sender.Send(ctx, mailer.Message{Brand: b, To: c.Email, Subject: "Do you still want emails from " + b.Name + "?",
		HTML: html, Text: text, Kind: "repermission",
		Headers: map[string]string{"List-Unsubscribe": lu, "List-Unsubscribe-Post": lup}})
}

// Outcome of a confirmation click.
type Outcome string

const (
	Confirmed Outcome = "confirmed"
	Already   Outcome = "already" // was already subscribed
	CantJoin  Outcome = "cant"    // unsubscribed meanwhile, suppressed, or gone
)

// View is what the confirm page shows.
type View struct {
	Brand   store.Brand
	Contact *store.Contact
	Purpose Purpose
}

// Load reads what the page needs (no changes).
func (s *Service) Load(ctx context.Context, c Claims) (View, error) {
	v := View{Purpose: c.Purpose}
	b, err := s.store.BrandByID(ctx, c.BrandID)
	if err != nil {
		return v, err
	}
	v.Brand = b
	contact, err := s.store.ContactByID(ctx, c.BrandID, c.ContactID)
	if err == nil {
		v.Contact = &contact
	} else if !errors.Is(err, store.ErrNotFound) {
		return v, err
	}
	return v, nil
}

// Confirm applies a click: pending (opt-in link) or quarantined
// (re-permission link) becomes subscribed, with a consent record.
func (s *Service) Confirm(ctx context.Context, c Claims, ip string) (View, Outcome, error) {
	var out Outcome
	var v View
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		b, err := tx.BrandByID(ctx, c.BrandID)
		if err != nil {
			return err
		}
		v = View{Brand: b, Purpose: c.Purpose}
		contact, err := tx.LockContact(ctx, c.BrandID, c.ContactID)
		if errors.Is(err, store.ErrNotFound) {
			out = CantJoin
			return nil
		}
		if err != nil {
			return err
		}
		v.Contact = &contact
		from := []store.ContactStatus{store.ContactPending}
		source := "double opt-in link"
		if c.Purpose == PurposeRepermission {
			from, source = []store.ContactStatus{store.ContactQuarantined}, "re-permission link"
		}
		switch {
		case contact.SuppressionReason != nil:
			out = CantJoin
			return nil
		case contact.Status == store.ContactSubscribed:
			out = Already
			return nil
		case !slices.Contains(from, contact.Status):
			out = CantJoin
			return nil
		}
		now := s.now()
		if err := tx.ConfirmContact(ctx, contact.ID, from, now); err != nil {
			return err
		}
		out = Confirmed
		return tx.AddConsentRecord(ctx, store.NewConsentRecord{
			BrandID: b.ID, ContactID: contact.ID, Email: contact.Email, Kind: "confirmed", Method: "link",
			Source: source, IP: ip, OccurredAt: now,
		})
	})
	return v, out, err
}

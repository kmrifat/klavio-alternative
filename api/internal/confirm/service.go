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
	"strings"
	texttemplate "text/template"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/brakes"
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
	gate     Gate
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
	Override string
	Log      *slog.Logger
}

func NewService(d Deps) *Service {
	return &Service{store: d.Store, signer: d.Signer, unsub: d.Unsub, sender: d.Sender, caps: d.Caps, warm: d.Warmup,
		override: d.Override, log: d.Log, now: time.Now}
}

// SetSender swaps the sender (the SES sender, once it exists).
func (s *Service) SetSender(m mailer.Sender) { s.sender = m }

// Gate is the brakes' say (M4's *brakes.Service).
type Gate interface {
	CheckGate(ctx context.Context, tx *store.Store, brandID uuid.UUID) (brakes.Gate, error)
}

// SetGate makes these emails respect the brakes: confirmations stop under
// the account brake or a paused brand; re-permission emails (the riskiest
// mail there is) go out only while the brand's brake is off.
func (s *Service) SetGate(g Gate) { s.gate = g }

// allowed asks the brakes; healthyOnly also refuses watch and slowed.
func (s *Service) allowed(ctx context.Context, brandID uuid.UUID, healthyOnly bool) (bool, string, error) {
	if s.gate == nil {
		return true, "", nil
	}
	g, err := s.gate.CheckGate(ctx, nil, brandID)
	if err != nil {
		return false, "", err
	}
	if !g.Allowed || (healthyOnly && (g.RateFactor < 1 || g.BlockInactive || g.NeedsApproval)) {
		return false, g.Reason, nil
	}
	return true, "", nil
}

type emailData struct {
	Brand      store.Brand
	Contact    store.Contact
	Topics     []string // what they're confirming
	NewTopics  bool     // a subscriber confirming extra topics
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

// SendConfirmation emails a confirmation link (P2). requestID Nil: a
// sign-up awaiting double opt-in. Otherwise a subscriber's topic request,
// whose link confirms exactly that request's topics. People who confirmed,
// left or were suppressed meanwhile get nothing. Run by the confirmation job.
func (s *Service) SendConfirmation(ctx context.Context, brandID, contactID, requestID uuid.UUID) error {
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
	if c.SuppressionReason != nil {
		return nil
	}
	if ok, why, err := s.allowed(ctx, b.ID, false); err != nil || !ok {
		if err == nil {
			s.log.WarnContext(ctx, "confirmation email not sent: brakes", "brand", b.Slug, "reason", why)
		}
		return err
	}
	now := s.now()
	claims := Claims{BrandID: b.ID, ContactID: c.ID, Purpose: PurposeOptIn, IssuedAt: now.Truncate(time.Second)}
	d := emailData{Brand: b, Contact: c, Days: int(TTL.Hours() / 24)}
	subject := "Please confirm your subscription to " + b.Name
	if requestID == uuid.Nil {
		if c.Status != store.ContactPending {
			return nil
		}
		if d.Topics, err = s.contactTopicNames(ctx, c); err != nil {
			return err
		}
	} else {
		req, err := s.store.TopicRequestByID(ctx, b.ID, c.ID, requestID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if c.Status != store.ContactSubscribed || req.AppliedAt != nil {
			return nil
		}
		if d.Topics, err = s.store.TopicNames(ctx, b.ID, req.TopicIDs); err != nil {
			return err
		}
		claims.Purpose, claims.RequestID, d.NewTopics = PurposeTopics, req.ID, true
		subject = "Please confirm your new topics at " + b.Name
	}
	d.ConfirmURL = s.signer.URL(s.base(b), claims)
	html, text, err := render("confirmation", d)
	if err != nil {
		return err
	}
	err = s.sender.Send(ctx, mailer.Message{Brand: b, To: c.Email, Subject: subject, HTML: html, Text: text, Kind: "confirmation"})
	if errors.Is(err, mailer.ErrOutcomeUnknown) {
		// It may have gone out: count it as sent, don't retry. Someone who
		// didn't get it can ask again.
		s.log.WarnContext(ctx, "confirmation email outcome unknown; not retried", "brand", b.Slug, "contact_id", c.ID, "err", err)
		err = nil
	}
	if err != nil {
		return fmt.Errorf("sending confirmation: %w", err)
	}
	return s.store.SetConfirmationSent(ctx, c.ID, now)
}

func (s *Service) contactTopicNames(ctx context.Context, c store.Contact) ([]string, error) {
	ids, err := s.store.ContactTopicIDs(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	return s.store.TopicNames(ctx, c.BrandID, ids)
}

// CanSend: is a real sender configured? Re-permission stays off without one,
// so nothing is reserved (warm-up, frequency cap) for emails that can't go.
func (s *Service) CanSend() bool {
	_, unconfigured := s.sender.(mailer.Unconfigured)
	return !unconfigured
}

// SendRepermissions is the hourly job: for each ready brand that turned
// re-permission on, ask quarantined contacts to confirm, up to the daily
// limit (repermission_emails_per_day), within the warm-up cap and the
// cross-brand frequency cap.
func (s *Service) SendRepermissions(ctx context.Context) error {
	if !s.CanSend() {
		return nil
	}
	all, err := s.store.ListBrands(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, b := range all {
		if !b.RepermissionOn || b.Status != store.BrandReady {
			continue
		}
		if ok, why, err := s.allowed(ctx, b.ID, true); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", b.Slug, err))
			continue
		} else if !ok {
			s.log.InfoContext(ctx, "re-permission emails held back by the brakes", "brand", b.Slug, "reason", why)
			continue
		}
		if err := s.repermissionBrand(ctx, b); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", b.Slug, err))
		}
	}
	return errors.Join(errs...)
}

// repermissionBrand claims a batch under a per-brand lock (so parallel runs
// can't overshoot the daily limit), sends it, and gives back the claim, the
// warm-up allowance and the frequency-cap record of every email that fails.
//
// While the transaction is open every query goes through it: a second pool
// connection, wanted while holding one, deadlocks a small pool. If another
// run holds the brand's lock it's already sending; this one skips the brand
// instead of queueing a connection behind it. Emails go out after commit.
func (s *Service) repermissionBrand(ctx context.Context, b store.Brand) error {
	now := s.now()
	y, m, d := now.UTC().Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	var batch []store.Contact
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		locked, err := tx.TryLock(ctx, "repermission:"+b.ID.String())
		if err != nil || !locked {
			return err
		}
		sent, err := tx.RepermissionSentSince(ctx, b.ID, today)
		if err != nil {
			return err
		}
		perDay, err := limits.Read(ctx, tx, limits.RepermissionEmailsPerDay)
		if err != nil {
			return err
		}
		room := int(perDay) - sent
		if room <= 0 {
			return nil
		}
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
		if extra := granted - len(allowed); extra > 0 {
			if err := s.warm.Release(ctx, tx, b.ID, now, extra); err != nil {
				return err
			}
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
	var sendErr error
	for _, c := range batch {
		err := s.sendRepermission(ctx, b, c, now)
		if errors.Is(err, mailer.ErrOutcomeUnknown) {
			// May have gone out: keep it as sent (and its reservations).
			s.log.WarnContext(ctx, "re-permission email outcome unknown; not retried", "brand", b.Slug, "contact_id", c.ID, "err", err)
			continue
		}
		if err != nil {
			s.log.WarnContext(ctx, "re-permission email", "brand", b.Slug, "err", err)
			failed, sendErr = append(failed, c.ID), err
		}
	}
	if len(failed) > 0 {
		// Undo everything the failed emails reserved: they never went out.
		err := s.store.InTx(context.WithoutCancel(ctx), func(tx *store.Store) error {
			if err := tx.UnclaimRepermission(ctx, failed); err != nil {
				return err
			}
			if err := s.warm.Release(ctx, tx, b.ID, now, len(failed)); err != nil {
				return err
			}
			return s.caps.Release(ctx, tx, b.ID, failed, now)
		})
		return errors.Join(err, fmt.Errorf("%d of %d re-permission emails failed: %w", len(failed), len(batch), sendErr))
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
	// Topics the click confirms, shown before the button.
	Topics []string
	// Done: nothing left for this link to confirm (a topic request already applied).
	Done bool
}

// TopicsLink: the link confirms a subscriber's new topics (page wording).
func (v View) TopicsLink() bool { return v.Purpose == PurposeTopics }

// Load reads what the page needs (no changes).
func (s *Service) Load(ctx context.Context, c Claims) (View, error) {
	v := View{Purpose: c.Purpose}
	b, err := s.store.BrandByID(ctx, c.BrandID)
	if err != nil {
		return v, err
	}
	v.Brand = b
	contact, err := s.store.ContactByID(ctx, c.BrandID, c.ContactID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return v, nil
	case err != nil:
		return v, err
	}
	v.Contact = &contact
	if c.Purpose == PurposeTopics {
		req, err := s.store.TopicRequestByID(ctx, c.BrandID, contact.ID, c.RequestID)
		if errors.Is(err, store.ErrNotFound) {
			return v, store.ErrNotFound
		}
		if err != nil {
			return v, err
		}
		v.Done = req.AppliedAt != nil
		v.Topics, err = s.store.TopicNames(ctx, c.BrandID, req.TopicIDs)
		return v, err
	}
	v.Topics, err = s.contactTopicNames(ctx, contact)
	return v, err
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
		now := s.now()
		switch {
		case contact.SuppressionReason != nil:
			out = CantJoin
			return nil
		case c.Purpose == PurposeTopics:
			// Exactly the topics of this link's request; others wait for their own links.
			if contact.Status != store.ContactSubscribed {
				out = CantJoin
				return nil
			}
			req, err := tx.TopicRequestByID(ctx, c.BrandID, contact.ID, c.RequestID)
			if errors.Is(err, store.ErrNotFound) {
				out = CantJoin
				return nil
			}
			if err != nil {
				return err
			}
			if req.AppliedAt != nil {
				out = Already
				return nil
			}
			if _, err := tx.ApplyTopicRequest(ctx, req, now); err != nil {
				return err
			}
			names, err := tx.TopicNames(ctx, c.BrandID, req.TopicIDs)
			if err != nil {
				return err
			}
			source = "topic request link (" + strings.Join(names, ", ") + ")"
		case contact.Status == store.ContactSubscribed:
			out = Already
			return nil
		case !slices.Contains(from, contact.Status):
			out = CantJoin
			return nil
		default:
			if err := tx.ConfirmContact(ctx, contact.ID, from, now); err != nil {
				return err
			}
		}
		out = Confirmed
		return tx.AddConsentRecord(ctx, store.NewConsentRecord{
			BrandID: b.ID, ContactID: contact.ID, Email: contact.Email, Kind: "confirmed", Method: "link",
			Source: source, IP: ip, OccurredAt: now,
		})
	})
	return v, out, err
}

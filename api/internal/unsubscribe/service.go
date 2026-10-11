package unsubscribe

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// Service applies what recipients choose on the public pages. Every change
// writes a consent record; nothing here is reachable without a valid token.
type Service struct {
	store *store.Store
	now   func() time.Time
}

func NewService(st *store.Store) *Service { return &Service{store: st, now: time.Now} }

// View is everything a public page shows.
type View struct {
	Brand      store.Brand
	Contact    *store.Contact // nil: no longer a contact (erased, or never imported)
	Topics     []store.Topic  // all of the brand's topics
	Subscribed []uuid.UUID    // topics the contact is subscribed to
	Topic      *store.Topic   // the topic the link was about, if any
	Suppressed bool           // blocked company-wide (bounce, complaint, removal)
	// LongAgo: they unsubscribed more than UndoWindow ago. A link from an
	// old or forwarded email mustn't sign them up again: only a new sign-up
	// (with a confirmation) does.
	LongAgo bool
}

// UndoWindow: how long after unsubscribing someone can pick topics again
// from an email's link ("unsubscribed by mistake?").
const UndoWindow = 30 * 24 * time.Hour

func (v View) IsSubscribed(topicID uuid.UUID) bool { return slices.Contains(v.Subscribed, topicID) }

// CanAdd: may this person pick topics here? Only if they're properly
// subscribed, or were before they unsubscribed (within UndoWindow). People awaiting double opt-in
// or quarantined must confirm first; suppressed addresses never can.
func (v View) CanAdd() bool {
	if v.Contact == nil || v.Suppressed {
		return false
	}
	switch v.Contact.Status {
	case store.ContactSubscribed:
		return true
	case store.ContactUnsubscribed:
		return !v.LongAgo && v.Contact.StatusBeforeUnsubscribe != nil && *v.Contact.StatusBeforeUnsubscribe == store.ContactSubscribed
	}
	return false
}

// Unsubscribed: nothing left that we'd send them from this brand.
func (v View) Unsubscribed() bool {
	return v.Contact == nil || v.Suppressed || v.Contact.Status == store.ContactUnsubscribed || len(v.Subscribed) == 0
}

// errUnknownBrand: a correctly signed token for a brand that's gone.
var errUnknownBrand = errors.New("brand not found")

func (s *Service) load(ctx context.Context, tx *store.Store, c Claims, lock bool) (View, error) {
	var v View
	var err error
	v.Brand, err = tx.BrandByID(ctx, c.BrandID)
	if errors.Is(err, store.ErrNotFound) {
		return v, errUnknownBrand
	}
	if err != nil {
		return v, err
	}
	if v.Topics, err = tx.ListTopics(ctx, v.Brand.ID); err != nil {
		return v, err
	}
	if c.TopicID != uuid.Nil {
		if j := slices.IndexFunc(v.Topics, func(t store.Topic) bool { return t.ID == c.TopicID }); j >= 0 {
			v.Topic = &v.Topics[j]
		}
	}
	var contact store.Contact
	if lock {
		contact, err = tx.LockContact(ctx, v.Brand.ID, c.ContactID)
	} else {
		contact, err = tx.ContactByID(ctx, v.Brand.ID, c.ContactID)
	}
	if errors.Is(err, store.ErrNotFound) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.Contact = &contact
	v.Suppressed = contact.SuppressionReason != nil
	if contact.Status == store.ContactUnsubscribed {
		at, err := tx.LastUnsubscribedAt(ctx, contact.ID)
		if err != nil {
			return v, err
		}
		v.LongAgo = at == nil || s.now().Sub(*at) > UndoWindow
	}
	v.Subscribed, err = tx.ContactTopicIDs(ctx, contact.ID)
	return v, err
}

// Load is read-only: GET requests (and link scanners) never change anything.
func (s *Service) Load(ctx context.Context, c Claims) (View, error) {
	return s.load(ctx, s.store, c, false)
}

// Request describes where a change came from, for the consent record.
type Request struct {
	Method string // "header" (RFC 8058 one-click) or "link" (a page)
	IP     string
}

func campaignRef(c Claims) *uuid.UUID {
	if c.CampaignID == uuid.Nil {
		return nil
	}
	id := c.CampaignID
	return &id
}

// Unsubscribe removes the link's topic, or every topic when the link has no
// topic or all is true. It's idempotent: repeating it changes nothing and
// records nothing.
func (s *Service) Unsubscribe(ctx context.Context, c Claims, all bool, req Request) (View, error) {
	var v View
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		var err error
		if v, err = s.load(ctx, tx, c, true); err != nil || v.Contact == nil {
			return err
		}
		remove := v.Subscribed
		scope := "all topics"
		if !all && v.Topic != nil {
			remove = []uuid.UUID{v.Topic.ID}
			scope = v.Topic.Name
		}
		wasSubscribed := v.Contact.Status != store.ContactUnsubscribed
		removing := slices.ContainsFunc(remove, v.IsSubscribed)
		if err := tx.RemoveContactTopics(ctx, v.Contact.ID, remove); err != nil {
			return err
		}
		left, err := tx.ContactTopicIDs(ctx, v.Contact.ID)
		if err != nil {
			return err
		}
		becomesUnsubscribed := len(left) == 0 && wasSubscribed
		if becomesUnsubscribed {
			if err := tx.SetContactStatus(ctx, v.Contact.ID, store.ContactUnsubscribed); err != nil {
				return err
			}
			v.Contact.Status = store.ContactUnsubscribed
		}
		v.Subscribed = left
		if !removing && !becomesUnsubscribed {
			return nil // already done: no duplicate consent records
		}
		source := "Unsubscribe page: " + scope
		if req.Method == "header" {
			source = "One-click unsubscribe (email client): " + scope
		}
		return tx.AddConsentRecord(ctx, store.NewConsentRecord{
			BrandID: v.Brand.ID, ContactID: v.Contact.ID, Email: v.Contact.Email, Kind: "unsubscribed",
			Method: req.Method, Source: source, IP: req.IP, OccurredAt: s.now(), CampaignID: campaignRef(c),
		})
	})
	return v, err
}

// ErrCantSubscribe: this person may only remove topics here (see View.CanAdd).
var ErrCantSubscribe = errors.New("this address can't be subscribed")

// SavePreferences makes the contact's topics exactly `want`. Adding a topic
// is the person opting in themselves, from a link only they received; an
// unsubscribed contact who picks topics is subscribed again. Suppressed
// addresses can only remove.
func (s *Service) SavePreferences(ctx context.Context, c Claims, want []uuid.UUID, req Request) (View, error) {
	var v View
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		var err error
		if v, err = s.load(ctx, tx, c, true); err != nil || v.Contact == nil {
			return err
		}
		valid := map[uuid.UUID]string{}
		for _, t := range v.Topics {
			valid[t.ID] = t.Name
		}
		var add, remove []uuid.UUID
		for _, id := range want {
			if _, ok := valid[id]; ok && !v.IsSubscribed(id) && !slices.Contains(add, id) {
				add = append(add, id)
			}
		}
		for _, id := range v.Subscribed {
			if !slices.Contains(want, id) {
				remove = append(remove, id)
			}
		}
		if len(add) > 0 && !v.CanAdd() {
			return ErrCantSubscribe
		}
		now := s.now()
		record := func(kind, source string) error {
			return tx.AddConsentRecord(ctx, store.NewConsentRecord{
				BrandID: v.Brand.ID, ContactID: v.Contact.ID, Email: v.Contact.Email, Kind: kind,
				Method: "link", Source: source, IP: req.IP, OccurredAt: now, CampaignID: campaignRef(c),
			})
		}
		names := func(ids []uuid.UUID) string {
			out := ""
			for i, id := range ids {
				if i > 0 {
					out += ", "
				}
				out += valid[id]
			}
			return out
		}

		if len(remove) > 0 {
			if err := tx.RemoveContactTopics(ctx, v.Contact.ID, remove); err != nil {
				return err
			}
			if err := record("unsubscribed", "Preference center: removed "+names(remove)); err != nil {
				return err
			}
		}
		if len(add) > 0 {
			if err := tx.AddContactTopics(ctx, v.Contact.ID, add); err != nil {
				return err
			}
			kind := "opt_in"
			if v.Contact.Status == store.ContactUnsubscribed {
				kind = "resubscribed"
				if err := tx.Resubscribe(ctx, v.Contact.ID, now); errors.Is(err, store.ErrNotFound) {
					return ErrCantSubscribe
				} else if err != nil {
					return err
				}
				v.Contact.Status = store.ContactSubscribed
			}
			if err := record(kind, "Preference center: added "+names(add)); err != nil {
				return err
			}
		}
		left, err := tx.ContactTopicIDs(ctx, v.Contact.ID)
		if err != nil {
			return err
		}
		if len(left) == 0 && v.Contact.Status != store.ContactUnsubscribed {
			if err := tx.SetContactStatus(ctx, v.Contact.ID, store.ContactUnsubscribed); err != nil {
				return err
			}
			v.Contact.Status = store.ContactUnsubscribed
		}
		v.Subscribed = left
		return nil
	})
	if err != nil && !errors.Is(err, ErrCantSubscribe) && !errors.Is(err, errUnknownBrand) {
		return v, fmt.Errorf("saving preferences: %w", err)
	}
	return v, err
}

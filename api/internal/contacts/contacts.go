// Package contacts serves Contacts (S3): the list with engagement tiers and
// consent proof, adding one contact, unsubscribing, suppressing, and topics.
package contacts

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/brands"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/imports"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

type Handlers struct {
	Store *store.Store
	Log   *slog.Logger
}

func (h *Handlers) Routes(mux *http.ServeMux) {
	f := func(fn httpx.HandlerFunc) http.HandlerFunc { return httpx.Handle(h.Log, fn) }
	mux.HandleFunc("GET /admin/v1/brands/{slug}/contacts", f(h.list))
	mux.HandleFunc("GET /admin/v1/brands/{slug}/contacts/stats", f(h.stats))
	mux.HandleFunc("POST /admin/v1/brands/{slug}/contacts", f(h.add))
	mux.HandleFunc("GET /admin/v1/brands/{slug}/contacts/{id}", f(h.get))
	mux.HandleFunc("POST /admin/v1/brands/{slug}/contacts/{id}/unsubscribe", f(h.unsubscribe))
	mux.HandleFunc("POST /admin/v1/brands/{slug}/contacts/{id}/suppress", f(h.suppress))
	mux.HandleFunc("GET /admin/v1/brands/{slug}/topics", f(h.topics))
	mux.HandleFunc("POST /admin/v1/brands/{slug}/topics", f(h.addTopic))
}

// list needs editor or above: viewers only get reports without personal data.
func (h *Handlers) list(w http.ResponseWriter, r *http.Request) error {
	b, _, err := brands.WithRole(r, h.Store, store.RoleEditor)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	f := store.ContactFilter{
		Status: store.ContactStatus(q.Get("status")),
		Tag:    q.Get("tag"),
		Query:  q.Get("q"),
		After:  q.Get("after"),
	}
	for _, t := range strings.Split(q.Get("tier"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			if !store.Tier(t).Valid() {
				return httpx.Errorf(http.StatusBadRequest, "bad_tier", "unknown tier %q", t)
			}
			f.Tiers = append(f.Tiers, store.Tier(t))
		}
	}
	if v := q.Get("suppressed"); v != "" {
		sup := v == "true"
		f.Suppressed = &sup
	}
	f.Limit = 50
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 200 {
		f.Limit = n
	}
	list, err := h.Store.ListContacts(r.Context(), b.ID, f)
	if err != nil {
		return err
	}
	if list == nil {
		list = []store.Contact{}
	}
	next := "" // a full page means there may be more
	if len(list) == f.Limit {
		next = list[len(list)-1].Email
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"contacts": list, "next_after": next})
	return nil
}

// stats are counts only, so every role can see this brand's. The
// company-wide suppression count spans other brands: editors and up only.
func (h *Handlers) stats(w http.ResponseWriter, r *http.Request) error {
	b, p, err := brands.WithRole(r, h.Store, store.RoleViewer)
	if err != nil {
		return err
	}
	st, err := h.Store.ContactStats(r.Context(), b.ID)
	if err != nil {
		return err
	}
	if !p.HasBrandRole(b.ID, store.RoleEditor) && !p.SeesAllBrands() {
		st.SuppressedGlobal = nil
	}
	httpx.WriteJSON(w, http.StatusOK, st)
	return nil
}

type addInput struct {
	Email     string      `json:"email"`
	FirstName string      `json:"first_name"`
	LastName  string      `json:"last_name"`
	Tags      []string    `json:"tags"`
	TopicIDs  []uuid.UUID `json:"topic_ids"`
	// TrackingAllowed defaults to true; false for people (EU) who didn't
	// agree to click tracking.
	TrackingAllowed *bool `json:"tracking_allowed"`
	Consent         struct {
		Basis       string `json:"basis"`  // double_opt_in | single_opt_in | soft_opt_in
		Source      string `json:"source"` // how they agreed, e.g. "Asked at the shop counter"
		ConsentedAt string `json:"consented_at"`
	} `json:"consent"`
}

var errBad = func(code, msg string) error { return httpx.Errorf(http.StatusBadRequest, code, "%s", msg) }

// add creates one contact with a recorded consent source. Brand admins only,
// like imports.
func (h *Handlers) add(w http.ResponseWriter, r *http.Request) error {
	b, p, err := brands.WithRole(r, h.Store, store.RoleBrandAdmin)
	if err != nil {
		return err
	}
	var in addInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	email := imports.NormalizeEmail(in.Email)
	switch {
	case email == "":
		return errBad("invalid_email", "that isn't a valid email address")
	case in.Consent.Basis != "double_opt_in" && in.Consent.Basis != "single_opt_in" && in.Consent.Basis != "soft_opt_in":
		return errBad("bad_consent", "choose how they agreed")
	case strings.TrimSpace(in.Consent.Source) == "":
		return errBad("bad_consent", "say where and how they agreed — it's kept as proof")
	case len(in.TopicIDs) == 0:
		return errBad("no_topics", "choose at least one topic")
	}
	consentedAt := time.Now()
	if in.Consent.ConsentedAt != "" {
		t := imports.ParseDate(in.Consent.ConsentedAt)
		if t == nil || t.After(time.Now()) {
			return errBad("bad_consent", "consent date must be a past date like 2026-03-12")
		}
		consentedAt = *t
	}
	ctx := r.Context()
	if err := h.checkTopics(r, b.ID, in.TopicIDs); err != nil {
		return err
	}

	var id uuid.UUID
	err = h.Store.InTx(ctx, func(tx *store.Store) error {
		sup, err := tx.SuppressedHashes(ctx, [][]byte{store.EmailHash(email)})
		if err != nil {
			return err
		}
		if len(sup) > 0 {
			return httpx.Errorf(http.StatusConflict, "suppressed", "%s is blocked for every brand after a bounce, complaint or removal request", email)
		}
		id, err = tx.InsertContact(ctx, store.NewContact{
			BrandID: b.ID, Email: email, FirstName: strings.TrimSpace(in.FirstName), LastName: strings.TrimSpace(in.LastName),
			Status: store.ContactSubscribed, ConsentBasis: in.Consent.Basis, ConsentSource: strings.TrimSpace(in.Consent.Source),
			ConsentedAt: &consentedAt, Tags: imports.ParseTags(strings.Join(in.Tags, ",")),
			TrackingAllowed: in.TrackingAllowed == nil || *in.TrackingAllowed,
		})
		if errors.Is(err, store.ErrConflict) {
			return httpx.Errorf(http.StatusConflict, "contact_exists", "%s is already a contact", email)
		}
		if err != nil {
			return err
		}
		if err := tx.AddContactTopics(ctx, id, in.TopicIDs); err != nil {
			return err
		}
		if err := tx.AddConsentRecord(ctx, store.NewConsentRecord{
			BrandID: b.ID, ContactID: id, Email: email, Kind: "opt_in", Method: "manual",
			Source: strings.TrimSpace(in.Consent.Source), OccurredAt: consentedAt, RecordedBy: &p.User.ID,
			IP: httpx.ClientIP(r),
		}); err != nil {
			return err
		}
		return tx.Audit(ctx, auth.AuditEntry(r, "contact.added", "contact", email, &b.ID), nil,
			map[string]any{"consent_basis": in.Consent.Basis, "source": in.Consent.Source})
	})
	if err != nil {
		return err
	}
	c, err := h.Store.ContactByID(ctx, b.ID, id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, c)
	return nil
}

func (h *Handlers) checkTopics(r *http.Request, brandID uuid.UUID, ids []uuid.UUID) error {
	topics, err := h.Store.ListTopics(r.Context(), brandID)
	if err != nil {
		return err
	}
	ok := map[uuid.UUID]bool{}
	for _, t := range topics {
		ok[t.ID] = true
	}
	for _, id := range ids {
		if !ok[id] {
			return errBad("unknown_topic", "that topic doesn't belong to this brand")
		}
	}
	return nil
}

func (h *Handlers) contact(r *http.Request, min store.BrandRole) (store.Brand, *auth.Principal, store.Contact, error) {
	b, p, err := brands.WithRole(r, h.Store, min)
	if err != nil {
		return b, p, store.Contact{}, err
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return b, p, store.Contact{}, httpx.ErrNotFound
	}
	c, err := h.Store.ContactByID(r.Context(), b.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		return b, p, c, httpx.ErrNotFound
	}
	return b, p, c, err
}

func (h *Handlers) get(w http.ResponseWriter, r *http.Request) error {
	_, _, c, err := h.contact(r, store.RoleEditor)
	if err != nil {
		return err
	}
	topics, err := h.Store.ContactTopicIDs(r.Context(), c.ID)
	if err != nil {
		return err
	}
	history, err := h.Store.ConsentHistory(r.Context(), c.ID)
	if err != nil {
		return err
	}
	if history == nil {
		history = []store.ConsentRecord{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"contact": c, "topic_ids": topics, "consent_history": history})
	return nil
}

// unsubscribe honours a request made outside the email (phone, reply…).
// Empty topic_ids = from everything. Editors can do it: saying no must be easy.
func (h *Handlers) unsubscribe(w http.ResponseWriter, r *http.Request) error {
	b, p, c, err := h.contact(r, store.RoleEditor)
	if err != nil {
		return err
	}
	var in struct {
		TopicIDs []uuid.UUID `json:"topic_ids"`
		Note     string      `json:"note"`
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	err = h.Store.InTx(ctx, func(tx *store.Store) error {
		current, err := tx.ContactTopicIDs(ctx, c.ID)
		if err != nil {
			return err
		}
		remove := in.TopicIDs
		if len(remove) == 0 {
			remove = current
		}
		if err := tx.RemoveContactTopics(ctx, c.ID, remove); err != nil {
			return err
		}
		left, err := tx.ContactTopicIDs(ctx, c.ID)
		if err != nil {
			return err
		}
		if len(left) == 0 {
			if err := tx.SetContactStatus(ctx, c.ID, store.ContactUnsubscribed); err != nil {
				return err
			}
		}
		if err := tx.AddConsentRecord(ctx, store.NewConsentRecord{
			BrandID: b.ID, ContactID: c.ID, Email: c.Email, Kind: "unsubscribed", Method: "manual",
			Source: strings.TrimSpace(in.Note), RecordedBy: &p.User.ID, IP: httpx.ClientIP(r),
		}); err != nil {
			return err
		}
		return tx.Audit(ctx, auth.AuditEntry(r, "contact.unsubscribed", "contact", c.Email, &b.ID),
			map[string]any{"topics": current}, map[string]any{"topics": left, "note": in.Note})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// suppress blocks the address for every brand, for good. Brand admins only.
func (h *Handlers) suppress(w http.ResponseWriter, r *http.Request) error {
	b, p, c, err := h.contact(r, store.RoleBrandAdmin)
	if err != nil {
		return err
	}
	var in struct {
		Note string `json:"note"`
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	if strings.TrimSpace(in.Note) == "" {
		return errBad("note_required", "say why, e.g. \"asked never to be emailed again\"")
	}
	ctx := r.Context()
	err = h.Store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.Suppress(ctx, c.Email, store.SuppressManual, &b.ID, in.Note, &p.User.ID); err != nil {
			return err
		}
		return tx.Audit(ctx, auth.AuditEntry(r, "contact.suppressed", "contact", c.Email, &b.ID), nil,
			map[string]string{"reason": "manual", "note": in.Note})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handlers) topics(w http.ResponseWriter, r *http.Request) error {
	b, _, err := brands.WithRole(r, h.Store, store.RoleViewer)
	if err != nil {
		return err
	}
	ts, err := h.Store.ListTopics(r.Context(), b.ID)
	if err != nil {
		return err
	}
	if ts == nil {
		ts = []store.Topic{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"topics": ts})
	return nil
}

func (h *Handlers) addTopic(w http.ResponseWriter, r *http.Request) error {
	b, _, err := brands.WithRole(r, h.Store, store.RoleBrandAdmin)
	if err != nil {
		return err
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 60 {
		return errBad("bad_name", "topic name must be 1–60 characters")
	}
	var t store.Topic
	err = h.Store.InTx(r.Context(), func(tx *store.Store) error {
		var err error
		t, err = tx.CreateTopic(r.Context(), b.ID, name)
		if errors.Is(err, store.ErrConflict) {
			return httpx.Errorf(http.StatusConflict, "topic_exists", "there's already a topic called %q", name)
		}
		if err != nil {
			return err
		}
		return tx.Audit(r.Context(), auth.AuditEntry(r, "topic.created", "topic", name, &b.ID), nil, nil)
	})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, t)
	return nil
}

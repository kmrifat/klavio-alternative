package segments

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/brands"
	"github.com/kmrifat/klavio-alternative/api/internal/cache"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

type Handlers struct {
	Store   *store.Store
	Limiter cache.Limiter
	Log     *slog.Logger
}

// PreviewsPerMinute limits live counts per person: each is a full scan.
const PreviewsPerMinute = 60

func (h *Handlers) Routes(mux *http.ServeMux) {
	f := func(fn httpx.HandlerFunc) http.HandlerFunc { return httpx.Handle(h.Log, fn) }
	mux.HandleFunc("GET /admin/v1/segments/fields", f(h.fields))
	mux.HandleFunc("GET /admin/v1/brands/{slug}/segments", f(h.list))
	mux.HandleFunc("POST /admin/v1/brands/{slug}/segments", f(h.create))
	mux.HandleFunc("POST /admin/v1/brands/{slug}/segments/preview", f(h.preview))
	mux.HandleFunc("GET /admin/v1/brands/{slug}/segments/{id}", f(h.get))
	mux.HandleFunc("PUT /admin/v1/brands/{slug}/segments/{id}", f(h.update))
	mux.HandleFunc("DELETE /admin/v1/brands/{slug}/segments/{id}", f(h.delete))
}

// Count compiles the rules and counts the brand's matching contacts.
func Count(ctx context.Context, st *store.Store, brandID uuid.UUID, r Rules) (store.SegmentCounts, error) {
	c, err := Compile(r, 2)
	if err != nil {
		return store.SegmentCounts{}, err
	}
	return st.CountSegment(ctx, brandID, c.SQL, c.Args)
}

// recount counts a saved segment and caches the result for the list.
func (h *Handlers) recount(ctx context.Context, b store.Brand, s *store.Segment) error {
	var rules Rules
	if err := json.Unmarshal(s.Rules, &rules); err != nil {
		return err
	}
	counts, err := Count(ctx, h.Store, b.ID, rules)
	if err != nil {
		return badRules(err)
	}
	if err := h.Store.SaveSegmentCounts(ctx, s.ID, counts); err != nil {
		return err
	}
	now := time.Now()
	s.Counts, s.CountedAt = &counts, &now
	return nil
}

func badRules(err error) error {
	var re *Error
	if errors.As(err, &re) {
		e := httpx.Errorf(http.StatusBadRequest, "invalid_rules", "%s", re.Error())
		return e
	}
	return err
}

func (h *Handlers) fields(w http.ResponseWriter, r *http.Request) error {
	if _, err := auth.Require(r); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"fields": Fields})
	return nil
}

// list shows each segment's last count (counting every segment on each
// visit would scan the contacts table once per segment).
func (h *Handlers) list(w http.ResponseWriter, r *http.Request) error {
	b, _, err := brands.WithRole(r, h.Store, store.RoleViewer)
	if err != nil {
		return err
	}
	segs, err := h.Store.ListSegments(r.Context(), b.ID)
	if err != nil {
		return err
	}
	if segs == nil {
		segs = []store.Segment{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"segments": segs})
	return nil
}

// get recounts the segment. Viewers may see a saved segment's numbers; only
// editors can try rules of their own (preview).
func (h *Handlers) get(w http.ResponseWriter, r *http.Request) error {
	b, _, err := brands.WithRole(r, h.Store, store.RoleViewer)
	if err != nil {
		return err
	}
	s, err := h.segment(r, b)
	if err != nil {
		return err
	}
	if err := h.recount(r.Context(), b, &s); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, s)
	return nil
}

func (h *Handlers) segment(r *http.Request, b store.Brand) (store.Segment, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return store.Segment{}, httpx.ErrNotFound
	}
	s, err := h.Store.SegmentByID(r.Context(), b.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		return s, httpx.ErrNotFound
	}
	return s, err
}

type input struct {
	Name  string `json:"name"`
	Rules Rules  `json:"rules"`
}

func (in *input) validate() (json.RawMessage, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 100 {
		return nil, httpx.Errorf(http.StatusBadRequest, "invalid_name", "give the segment a name (up to 100 characters)")
	}
	if len(in.Rules.Groups) == 0 {
		return nil, httpx.Errorf(http.StatusBadRequest, "invalid_rules", "add at least one condition")
	}
	if _, err := Compile(in.Rules, 2); err != nil {
		return nil, badRules(err)
	}
	return json.Marshal(in.Rules)
}

func conflict(err error) error {
	if errors.Is(err, store.ErrConflict) {
		return httpx.Errorf(http.StatusConflict, "name_taken", "another segment already has this name")
	}
	return err
}

func (h *Handlers) create(w http.ResponseWriter, r *http.Request) error {
	b, p, err := brands.WithRole(r, h.Store, store.RoleEditor)
	if err != nil {
		return err
	}
	var in input
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	raw, err := in.validate()
	if err != nil {
		return err
	}
	var s store.Segment
	err = h.Store.InTx(r.Context(), func(tx *store.Store) error {
		if s, err = tx.CreateSegment(r.Context(), b.ID, in.Name, raw, p.User.ID); err != nil {
			return conflict(err)
		}
		return tx.Audit(r.Context(), auth.AuditEntry(r, "segment.created", "segment", s.Name, &b.ID), nil, map[string]any{"rules": in.Rules})
	})
	if err != nil {
		return err
	}
	if err := h.recount(r.Context(), b, &s); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, s)
	return nil
}

func (h *Handlers) update(w http.ResponseWriter, r *http.Request) error {
	b, _, err := brands.WithRole(r, h.Store, store.RoleEditor)
	if err != nil {
		return err
	}
	old, err := h.segment(r, b)
	if err != nil {
		return err
	}
	var in input
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	raw, err := in.validate()
	if err != nil {
		return err
	}
	var s store.Segment
	err = h.Store.InTx(r.Context(), func(tx *store.Store) error {
		if s, err = tx.UpdateSegment(r.Context(), b.ID, old.ID, in.Name, raw); err != nil {
			return conflict(err)
		}
		return tx.Audit(r.Context(), auth.AuditEntry(r, "segment.updated", "segment", s.Name, &b.ID),
			map[string]any{"name": old.Name, "rules": old.Rules}, map[string]any{"name": s.Name, "rules": in.Rules})
	})
	if err != nil {
		return err
	}
	if err := h.recount(r.Context(), b, &s); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, s)
	return nil
}

func (h *Handlers) delete(w http.ResponseWriter, r *http.Request) error {
	b, _, err := brands.WithRole(r, h.Store, store.RoleEditor)
	if err != nil {
		return err
	}
	s, err := h.segment(r, b)
	if err != nil {
		return err
	}
	err = h.Store.InTx(r.Context(), func(tx *store.Store) error {
		if err := tx.DeleteSegment(r.Context(), b.ID, s.ID); err != nil {
			return err
		}
		return tx.Audit(r.Context(), auth.AuditEntry(r, "segment.deleted", "segment", s.Name, &b.ID), map[string]any{"rules": s.Rules}, nil)
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// preview is the live count while editing (S12), plus a few matching
// people. Editors and up only, like the contact list: a count of any rule
// someone can type is a yes/no oracle ("email starts with zo…"), which would
// spell out addresses for people not allowed to see them.
func (h *Handlers) preview(w http.ResponseWriter, r *http.Request) error {
	b, p, err := brands.WithRole(r, h.Store, store.RoleEditor)
	if err != nil {
		return err
	}
	ok, err := h.Limiter.Allow(r.Context(), "segments:preview:"+p.User.ID.String(), PreviewsPerMinute, time.Minute)
	if err != nil {
		return err
	}
	if !ok {
		return httpx.Errorf(http.StatusTooManyRequests, "rate_limited", "too many live counts; wait a minute")
	}
	var in struct {
		Rules Rules `json:"rules"`
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	c, err := Compile(in.Rules, 2)
	if err != nil {
		return badRules(err)
	}
	counts, err := h.Store.CountSegment(r.Context(), b.ID, c.SQL, c.Args)
	if err != nil {
		return err
	}
	sample, err := h.Store.SegmentSample(r.Context(), b.ID, c.SQL, c.Args, 10)
	if err != nil {
		return err
	}
	if sample == nil {
		sample = []store.Contact{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"counts": counts, "sample": sample})
	return nil
}

package segments

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/brands"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

type Handlers struct {
	Store *store.Store
	Log   *slog.Logger
}

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

type item struct {
	store.Segment
	Counts store.SegmentCounts `json:"counts"`
}

func (h *Handlers) list(w http.ResponseWriter, r *http.Request) error {
	b, _, err := brands.WithRole(r, h.Store, store.RoleViewer)
	if err != nil {
		return err
	}
	segs, err := h.Store.ListSegments(r.Context(), b.ID)
	if err != nil {
		return err
	}
	out := []item{}
	for _, s := range segs {
		var rules Rules
		_ = json.Unmarshal(s.Rules, &rules)
		counts, err := Count(r.Context(), h.Store, b.ID, rules)
		if err != nil && !errors.As(err, new(*Error)) {
			return err
		}
		out = append(out, item{Segment: s, Counts: counts})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"segments": out})
	return nil
}

func (h *Handlers) get(w http.ResponseWriter, r *http.Request) error {
	b, _, err := brands.WithRole(r, h.Store, store.RoleViewer)
	if err != nil {
		return err
	}
	s, err := h.segment(r, b)
	if err != nil {
		return err
	}
	var rules Rules
	_ = json.Unmarshal(s.Rules, &rules)
	counts, err := Count(r.Context(), h.Store, b.ID, rules)
	if err != nil {
		return badRules(err)
	}
	httpx.WriteJSON(w, http.StatusOK, item{Segment: s, Counts: counts})
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
	counts, err := Count(r.Context(), h.Store, b.ID, in.Rules)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, item{Segment: s, Counts: counts})
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
	counts, err := Count(r.Context(), h.Store, b.ID, in.Rules)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, item{Segment: s, Counts: counts})
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

// preview is the live count while editing (S12), plus a few matching people.
func (h *Handlers) preview(w http.ResponseWriter, r *http.Request) error {
	b, _, err := brands.WithRole(r, h.Store, store.RoleViewer)
	if err != nil {
		return err
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

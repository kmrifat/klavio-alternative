// Package privacy carries out GDPR erasure requests (Settings → Data &
// privacy): look up what's held about an address, erase it from every
// brand, and list the erasures done.
package privacy

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/imports"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

type stepUpper interface {
	RequireStepUp(r *http.Request) (*auth.Principal, error)
}

type Handlers struct {
	Store *store.Store
	Auth  stepUpper
	Log   *slog.Logger
}

// Owners and company approvers handle erasure requests: they concern every
// brand, and there's a legal deadline, so no second person is needed.
func (h *Handlers) Routes(mux *http.ServeMux) {
	f := func(fn httpx.HandlerFunc) http.HandlerFunc { return httpx.Handle(h.Log, fn) }
	mux.HandleFunc("GET /admin/v1/erasures", f(h.list))
	mux.HandleFunc("POST /admin/v1/erasures/lookup", f(h.lookup))
	mux.HandleFunc("POST /admin/v1/erasures", f(h.erase))
}

func admin(p *auth.Principal, err error) (*auth.Principal, error) {
	if err != nil {
		return nil, err
	}
	if !p.SeesAllBrands() {
		return nil, httpx.ErrForbidden
	}
	return p, nil
}

func email(raw string) (string, error) {
	e := imports.NormalizeEmail(raw)
	if e == "" {
		return "", httpx.Errorf(http.StatusBadRequest, "invalid_email", "that isn't an email address")
	}
	return e, nil
}

func (h *Handlers) list(w http.ResponseWriter, r *http.Request) error {
	if _, err := admin(auth.Require(r)); err != nil {
		return err
	}
	es, err := h.Store.ListErasures(r.Context(), 50)
	if err != nil {
		return err
	}
	if es == nil {
		es = []store.Erasure{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"erasures": es})
	return nil
}

// lookup is a POST so the address stays out of URLs and access logs.
func (h *Handlers) lookup(w http.ResponseWriter, r *http.Request) error {
	if _, err := admin(auth.Require(r)); err != nil {
		return err
	}
	var in struct {
		Email string `json:"email"`
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	e, err := email(in.Email)
	if err != nil {
		return err
	}
	data, err := h.Store.PersonData(r.Context(), e)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, data)
	return nil
}

func (h *Handlers) erase(w http.ResponseWriter, r *http.Request) error {
	p, err := admin(h.Auth.RequireStepUp(r))
	if err != nil {
		return err
	}
	var in struct {
		Email        string `json:"email"`
		ConfirmEmail string `json:"confirm_email"`
		Reference    string `json:"reference"`
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	e, err := email(in.Email)
	if err != nil {
		return err
	}
	if imports.NormalizeEmail(in.ConfirmEmail) != e {
		return httpx.Errorf(http.StatusBadRequest, "confirmation_mismatch", "type the same address again to confirm")
	}
	ref := strings.TrimSpace(in.Reference)
	switch {
	case ref == "":
		return httpx.Errorf(http.StatusBadRequest, "reference_required", "say where the request came from, e.g. \"Email to privacy@, 3 Oct\"")
	case len(ref) > 500:
		return httpx.Errorf(http.StatusBadRequest, "reference_too_long", "keep the reference under 500 characters")
	case strings.Contains(strings.ToLower(ref), e):
		// The reference is kept for good; it mustn't bring the address back.
		return httpx.Errorf(http.StatusBadRequest, "reference_names_person", "leave the email address out of the reference")
	}
	ctx := r.Context()
	var out store.Erasure
	err = h.Store.InTx(ctx, func(tx *store.Store) error {
		if out, err = tx.Erase(ctx, e, ref, p.User.ID); err != nil {
			return err
		}
		return tx.Audit(ctx, auth.AuditEntry(r, "contact.erased", "erasure", out.ID.String(), nil), nil,
			map[string]any{"contact_ref": out.ContactRef, "reference": ref, "summary": out.Summary})
	})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"erasure": out})
	return nil
}

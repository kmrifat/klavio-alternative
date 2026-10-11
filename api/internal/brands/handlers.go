package brands

import (
	"errors"
	"net/http"
	"strings"

	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

type authorizer interface {
	RequireStepUp(r *http.Request) (*auth.Principal, error)
}

func (s *Service) Routes(mux *http.ServeMux, az authorizer) {
	h := func(f httpx.HandlerFunc) http.HandlerFunc { return httpx.Handle(s.log, f) }

	mux.HandleFunc("GET /admin/v1/brands", h(s.list))
	mux.HandleFunc("POST /admin/v1/brands", h(func(w http.ResponseWriter, r *http.Request) error {
		p, err := az.RequireStepUp(r)
		if err != nil {
			return err
		}
		if !p.IsOwner() {
			return httpx.ErrForbidden
		}
		var in CreateInput
		if err := httpx.ReadJSON(r, &in); err != nil {
			return err
		}
		b, err := s.Create(r.Context(), r, in)
		if err != nil {
			return err
		}
		return s.writeDetail(w, r, http.StatusCreated, b)
	}))
	mux.HandleFunc("GET /admin/v1/brands/{slug}", h(func(w http.ResponseWriter, r *http.Request) error {
		b, _, err := s.visibleBrand(r)
		if err != nil {
			return err
		}
		return s.writeDetail(w, r, http.StatusOK, b)
	}))
	mux.HandleFunc("PATCH /admin/v1/brands/{slug}", h(s.update))
	// A check calls DNS and SES: editors and up, like the health re-check.
	mux.HandleFunc("POST /admin/v1/brands/{slug}/dns-check", h(func(w http.ResponseWriter, r *http.Request) error {
		b, _, err := WithRole(r, s.store, store.RoleEditor)
		if err != nil {
			return err
		}
		b, recs, err := s.Check(r.Context(), b)
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, detail{Brand: b, DNSRecords: recs})
		return nil
	}))
}

type listItem struct {
	store.Brand
	MyRole store.BrandRole `json:"my_role"`
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	p, err := auth.Require(r)
	if err != nil {
		return err
	}
	all, err := s.store.ListBrands(r.Context())
	if err != nil {
		return err
	}
	out := []listItem{}
	for _, b := range all {
		if !p.CanViewBrand(b.ID) {
			continue
		}
		role := p.Roles[b.ID]
		if p.IsOwner() {
			role = store.RoleBrandAdmin
		}
		out = append(out, listItem{Brand: b, MyRole: role})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"brands": out})
	return nil
}

// Visible loads the {slug} brand, answering 404 (not 403) for brands the
// user can't see so their existence isn't leaked. Other packages use it for
// their /brands/{slug}/… routes.
func Visible(r *http.Request, st *store.Store) (store.Brand, *auth.Principal, error) {
	p, err := auth.Require(r)
	if err != nil {
		return store.Brand{}, nil, err
	}
	b, err := st.BrandBySlug(r.Context(), r.PathValue("slug"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && !p.CanViewBrand(b.ID)) {
		return store.Brand{}, nil, httpx.ErrNotFound
	}
	return b, p, err
}

// WithRole is Visible plus a minimum brand role (approvers count as viewers).
func WithRole(r *http.Request, st *store.Store, min store.BrandRole) (store.Brand, *auth.Principal, error) {
	b, p, err := Visible(r, st)
	if err != nil {
		return b, p, err
	}
	if !p.HasBrandRole(b.ID, min) && !(min == store.RoleViewer && p.SeesAllBrands()) {
		return b, p, httpx.ErrForbidden
	}
	return b, p, nil
}

func (s *Service) visibleBrand(r *http.Request) (store.Brand, *auth.Principal, error) {
	return Visible(r, s.store)
}

type detail struct {
	Brand      store.Brand       `json:"brand"`
	DNSRecords []store.DNSRecord `json:"dns_records"`
}

func (s *Service) writeDetail(w http.ResponseWriter, r *http.Request, status int, b store.Brand) error {
	recs, err := s.store.DNSRecords(r.Context(), b.ID)
	if err != nil {
		return err
	}
	if recs == nil {
		recs = []store.DNSRecord{}
	}
	httpx.WriteJSON(w, status, detail{Brand: b, DNSRecords: recs})
	return nil
}

type updateInput struct {
	Name          *string `json:"name"`
	Color         *string `json:"color"`
	PostalAddress *string `json:"postal_address"`
}

// update changes name, color or postal address. Domains can't change: that
// would be a different sender with its own reputation, i.e. a new brand.
func (s *Service) update(w http.ResponseWriter, r *http.Request) error {
	b, p, err := s.visibleBrand(r)
	if err != nil {
		return err
	}
	if !p.HasBrandRole(b.ID, store.RoleBrandAdmin) {
		return httpx.ErrForbidden
	}
	var in updateInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	name, color, addr := b.Name, b.Color, b.PostalAddress
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	if in.Color != nil {
		color = strings.ToLower(strings.TrimSpace(*in.Color))
	}
	if in.PostalAddress != nil {
		addr = strings.TrimSpace(*in.PostalAddress)
	}
	switch {
	case name == "":
		return bad("invalid_name", "name is required")
	case !colorRe.MatchString(color):
		return bad("invalid_color", "color must look like #f97316")
	case addr == "":
		return bad("invalid_postal_address", "a postal address is required in every marketing email")
	}

	var updated store.Brand
	err = s.store.InTx(r.Context(), func(tx *store.Store) error {
		var err error
		if updated, err = tx.UpdateBrandDetails(r.Context(), b.ID, name, color, addr); err != nil {
			return err
		}
		before := map[string]string{"name": b.Name, "color": b.Color, "postal_address": b.PostalAddress}
		after := map[string]string{"name": name, "color": color, "postal_address": addr}
		return tx.Audit(r.Context(), auditFor(r, "brand.updated", b), before, after)
	})
	if err != nil {
		return err
	}
	return s.writeDetail(w, r, http.StatusOK, updated)
}

func auditFor(r *http.Request, action string, b store.Brand) store.AuditEntry {
	return auth.AuditEntry(r, action, "brand", b.Slug, &b.ID)
}

package approvals

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

type stepUpper interface {
	RequireStepUp(r *http.Request) (*auth.Principal, error)
}

func (s *Service) Routes(mux *http.ServeMux, az stepUpper) {
	h := func(f httpx.HandlerFunc) http.HandlerFunc { return httpx.Handle(s.log, f) }
	mux.HandleFunc("GET /admin/v1/approvals", h(s.list))
	// Approving runs someone else's action with your authority: confirm with a passkey.
	mux.HandleFunc("POST /admin/v1/approvals/{id}/approve", h(func(w http.ResponseWriter, r *http.Request) error {
		p, err := az.RequireStepUp(r)
		if err != nil {
			return err
		}
		return s.decideHTTP(w, r, p, store.ApprovalApproved)
	}))
	mux.HandleFunc("POST /admin/v1/approvals/{id}/reject", h(func(w http.ResponseWriter, r *http.Request) error {
		p, err := auth.Require(r)
		if err != nil {
			return err
		}
		return s.decideHTTP(w, r, p, store.ApprovalRejected)
	}))
	mux.HandleFunc("POST /admin/v1/approvals/{id}/cancel", h(s.cancel))
}

// Item is an approval with what the UI needs to show it.
type Item struct {
	store.Approval
	Label          string `json:"label"`
	RequesterName  string `json:"requester_name"`
	RequesterEmail string `json:"requester_email"`
	DeciderName    string `json:"decider_name"`
	BrandName      string `json:"brand_name"`
	BrandSlug      string `json:"brand_slug"`
	CanDecide      bool   `json:"can_decide"`
	Mine           bool   `json:"mine"`
}

func (s *Service) items(r *http.Request, p *auth.Principal, list []store.Approval) ([]Item, error) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		return nil, err
	}
	brands, err := s.store.ListBrands(r.Context())
	if err != nil {
		return nil, err
	}
	userByID := map[uuid.UUID]store.User{}
	for _, u := range users {
		userByID[u.ID] = u
	}
	brandByID := map[uuid.UUID]store.Brand{}
	for _, b := range brands {
		brandByID[b.ID] = b
	}
	now := s.now()
	out := []Item{}
	for _, a := range list {
		if !s.CanSee(p, a) {
			continue
		}
		if a.Status == store.ApprovalPending && !a.ExpiresAt.After(now) {
			a.Status = store.ApprovalExpired // the hourly job may not have run yet
		}
		it := Item{
			Approval: a, Label: s.Label(a.Kind),
			RequesterName: userByID[a.RequestedBy].Name, RequesterEmail: userByID[a.RequestedBy].Email,
			CanDecide: a.Status == store.ApprovalPending && s.CanDecide(p, a), Mine: a.RequestedBy == p.User.ID,
		}
		if a.DecidedBy != nil {
			it.DeciderName = userByID[*a.DecidedBy].Name
		}
		if a.BrandID != nil {
			it.BrandName, it.BrandSlug = brandByID[*a.BrandID].Name, brandByID[*a.BrandID].Slug
		}
		out = append(out, it)
	}
	return out, nil
}

// list: ?view=decide (waiting for me), mine (my requests), recent (everything I can see).
func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	p, err := auth.Require(r)
	if err != nil {
		return err
	}
	f := store.ApprovalFilter{Limit: 100}
	view := r.URL.Query().Get("view")
	switch view {
	case "decide":
		f.Status = store.ApprovalPending
	case "mine":
		f.RequestedBy = &p.User.ID
	}
	list, err := s.store.ListApprovals(r.Context(), f)
	if err != nil {
		return err
	}
	items, err := s.items(r, p, list)
	if err != nil {
		return err
	}
	if view == "decide" {
		kept := items[:0]
		for _, it := range items {
			if it.CanDecide {
				kept = append(kept, it)
			}
		}
		items = kept
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"approvals": items})
	return nil
}

func (s *Service) decideHTTP(w http.ResponseWriter, r *http.Request, p *auth.Principal, status store.ApprovalStatus) error {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return httpx.ErrNotFound
	}
	var in struct {
		Note string `json:"note"`
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	in.Note = strings.TrimSpace(in.Note)
	if status == store.ApprovalRejected && in.Note == "" {
		return httpx.Errorf(http.StatusBadRequest, "note_required", "say why, so the requester knows what to change")
	}
	a, err := s.decide(r.Context(), p, id, status, in.Note, auth.AuditEntry(r, "", "", "", nil))
	if err != nil {
		return err
	}
	items, err := s.items(r, p, []store.Approval{a})
	if err != nil || len(items) == 0 {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, items[0])
	return nil
}

func (s *Service) cancel(w http.ResponseWriter, r *http.Request) error {
	p, err := auth.Require(r)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return httpx.ErrNotFound
	}
	if err := s.Cancel(r.Context(), p, id, auth.AuditEntry(r, "", "", "", nil)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

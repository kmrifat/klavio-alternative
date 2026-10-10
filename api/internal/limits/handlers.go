package limits

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
)

func jsonUnmarshal(raw []byte, v any) error { return json.Unmarshal(raw, v) }

type stepUpper interface {
	RequireStepUp(r *http.Request) (*auth.Principal, error)
}

func (s *Service) Routes(mux *http.ServeMux, az stepUpper) {
	h := func(f httpx.HandlerFunc) http.HandlerFunc { return httpx.Handle(s.log, f) }
	mux.HandleFunc("GET /admin/v1/safety-limits", h(func(w http.ResponseWriter, r *http.Request) error {
		if _, err := auth.Require(r); err != nil {
			return err
		}
		all, err := s.All(r.Context())
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"limits": all})
		return nil
	}))
	// Owners and company approvers ("any admin") change limits, with a passkey confirmation.
	mux.HandleFunc("PUT /admin/v1/safety-limits", h(func(w http.ResponseWriter, r *http.Request) error {
		p, err := az.RequireStepUp(r)
		if err != nil {
			return err
		}
		if !p.SeesAllBrands() {
			return httpx.ErrForbidden
		}
		var in struct {
			Changes map[string]float64 `json:"changes"`
			Reason  string             `json:"reason"`
		}
		if err := httpx.ReadJSON(r, &in); err != nil {
			return err
		}
		res, err := s.Change(r.Context(), r, p, in.Changes, strings.TrimSpace(in.Reason))
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, res)
		return nil
	}))
}

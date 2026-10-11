// Package company serves Settings → General: the company's name and a
// read-only description of this installation.
package company

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

type stepUpper interface {
	RequireStepUp(r *http.Request) (*auth.Principal, error)
}

// Installation is how this server is set up. It comes from the environment
// and is shown, never edited, in the app.
type Installation struct {
	Version          string `json:"version"`
	Env              string `json:"env"`
	Debug            bool   `json:"debug"`
	AppURL           string `json:"app_url"`
	PasskeyDomain    string `json:"passkey_domain"`    // WEBAUTHN_RP_ID
	CloudflareAccess bool   `json:"cloudflare_access"` // every admin request needs a Cloudflare Access login too
}

type Handlers struct {
	Store        *store.Store
	Auth         stepUpper
	Log          *slog.Logger
	Installation Installation
	// DefaultName is shown until a name is set (WEBAUTHN_RP_NAME).
	DefaultName string
}

// MaxName fits the sidebar.
const MaxName = 80

func (h *Handlers) Routes(mux *http.ServeMux) {
	f := func(fn httpx.HandlerFunc) http.HandlerFunc { return httpx.Handle(h.Log, fn) }
	mux.HandleFunc("GET /admin/v1/company", f(h.get))
	mux.HandleFunc("PUT /admin/v1/company", f(h.rename))
}

// DisplayName is the name to show: the one set, or the default. A database
// error shows the default rather than breaking the sign-in page.
func (h *Handlers) DisplayName(ctx context.Context) string {
	name, err := h.Store.CompanyName(ctx)
	if err != nil {
		h.Log.WarnContext(ctx, "company name", "err", err)
	}
	if name == "" {
		return h.DefaultName
	}
	return name
}

type view struct {
	Name         string       `json:"name"`         // "" = not set
	DisplayName  string       `json:"display_name"` // what the app shows
	Installation Installation `json:"installation"`
}

func (h *Handlers) view(ctx context.Context) (view, error) {
	name, err := h.Store.CompanyName(ctx)
	if err != nil {
		return view{}, err
	}
	v := view{Name: name, DisplayName: name, Installation: h.Installation}
	if name == "" {
		v.DisplayName = h.DefaultName
	}
	return v, nil
}

func (h *Handlers) get(w http.ResponseWriter, r *http.Request) error {
	if _, err := auth.Require(r); err != nil {
		return err
	}
	v, err := h.view(r.Context())
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, v)
	return nil
}

// rename: owners only, with a passkey confirmation.
func (h *Handlers) rename(w http.ResponseWriter, r *http.Request) error {
	p, err := h.Auth.RequireStepUp(r)
	if err != nil {
		return err
	}
	if !p.IsOwner() {
		return httpx.ErrForbidden
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	name := strings.Join(strings.Fields(in.Name), " ")
	switch {
	case name == "":
		return httpx.Errorf(http.StatusBadRequest, "name_required", "enter the company's name")
	case utf8.RuneCountInString(name) > MaxName:
		return httpx.Errorf(http.StatusBadRequest, "name_too_long", "keep the name under %d characters", MaxName)
	}
	ctx := r.Context()
	err = h.Store.InTx(ctx, func(tx *store.Store) error {
		old, err := tx.SetCompanyName(ctx, name)
		if err != nil {
			return err
		}
		return tx.Audit(ctx, auth.AuditEntry(r, "company.renamed", "company", "", nil),
			map[string]string{"name": old}, map[string]string{"name": name})
	})
	if err != nil {
		return err
	}
	v, err := h.view(ctx)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, v)
	return nil
}

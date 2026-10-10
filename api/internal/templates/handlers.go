package templates

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/publicsuffix"

	"github.com/kmrifat/klavio-alternative/api/internal/approvals"
	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

type authorizer interface {
	RequireStepUp(r *http.Request) (*auth.Principal, error)
}

// Who may do what:
//   - anyone who can see the brand: read layout, allowlist and templates
//   - editor and up: create, edit, preview and archive templates
//   - brand admin, with a fresh passkey confirmation: change the layout or
//     the allowlist, because both apply to every email the brand sends
//
// Changes to the layout will also need a second admin's approval once the
// approvals engine lands (M5).
func (s *Service) Routes(mux *http.ServeMux, az authorizer) {
	h := func(f httpx.HandlerFunc) http.HandlerFunc { return httpx.Handle(s.log, compileErrors(f)) }
	withAZ := func(f func(http.ResponseWriter, *http.Request, authorizer) error) http.HandlerFunc {
		return h(func(w http.ResponseWriter, r *http.Request) error { return f(w, r, az) })
	}
	base := "/admin/v1/brands/{slug}"

	mux.HandleFunc("GET "+base+"/layout", h(s.getLayout))
	mux.HandleFunc("PUT "+base+"/layout", withAZ(s.putLayout))
	mux.HandleFunc("POST "+base+"/layout/preview", h(s.previewLayout))

	mux.HandleFunc("GET "+base+"/link-allowlist", h(s.getAllowlist))
	mux.HandleFunc("POST "+base+"/link-allowlist", withAZ(s.allowDomain))
	mux.HandleFunc("DELETE "+base+"/link-allowlist/{domain}", withAZ(s.disallowDomain))

	mux.HandleFunc("GET "+base+"/templates", h(s.listTemplates))
	mux.HandleFunc("POST "+base+"/templates", h(s.createTemplate))
	mux.HandleFunc("POST "+base+"/templates/preview", h(s.previewTemplate))
	mux.HandleFunc("GET "+base+"/templates/{id}", h(s.getTemplate))
	mux.HandleFunc("PUT "+base+"/templates/{id}", h(s.updateTemplate))
	mux.HandleFunc("DELETE "+base+"/templates/{id}", h(s.archiveTemplate))
}

// compileErrors turns a busy or slow compiler into a 503 the editor can retry.
func compileErrors(f httpx.HandlerFunc) httpx.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		err := f(w, r)
		switch {
		case errors.Is(err, ErrBusy):
			w.Header().Set("Retry-After", "5")
			return httpx.Errorf(http.StatusServiceUnavailable, "compiler_busy", "the template compiler is busy; try again in a few seconds")
		case errors.Is(err, ErrTimeout):
			return httpx.Errorf(http.StatusServiceUnavailable, "compile_timeout", "compiling took too long; make the template smaller")
		}
		return err
	}
}

// visibleBrand loads the {slug} brand, answering 404 (not 403) for brands the
// user can't see so their existence isn't leaked.
func (s *Service) visibleBrand(r *http.Request) (store.Brand, *auth.Principal, error) {
	p, err := auth.Require(r)
	if err != nil {
		return store.Brand{}, nil, err
	}
	b, err := s.store.BrandBySlug(r.Context(), r.PathValue("slug"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && !p.CanViewBrand(b.ID)) {
		return store.Brand{}, nil, httpx.ErrNotFound
	}
	return b, p, err
}

// brandWithRole is visibleBrand plus a minimum role.
func (s *Service) brandWithRole(r *http.Request, min store.BrandRole) (store.Brand, *auth.Principal, error) {
	b, p, err := s.visibleBrand(r)
	if err == nil && !p.HasBrandRole(b.ID, min) {
		err = httpx.ErrForbidden
	}
	return b, p, err
}

// adminWithStepUp is brandWithRole(brand admin) plus a fresh passkey confirmation.
func (s *Service) adminWithStepUp(r *http.Request, az authorizer) (store.Brand, *auth.Principal, error) {
	b, _, err := s.brandWithRole(r, store.RoleBrandAdmin)
	if err != nil {
		return b, nil, err
	}
	p, err := az.RequireStepUp(r)
	return b, p, err
}

func bad(code, format string, args ...any) error {
	return httpx.Errorf(http.StatusBadRequest, code, format, args...)
}

var errStale = httpx.Errorf(http.StatusConflict, "stale", "someone else saved a newer version; reload to see it")

// rejected is a save refused because of check errors. It carries the issues
// so the editor can show them.
func rejected(w http.ResponseWriter, code, message string, issues []Issue) error {
	httpx.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"error":  map[string]string{"code": code, "message": message},
		"issues": issues,
	})
	return nil
}

// --- layout ---

type layoutView struct {
	MJML      string     `json:"mjml"`
	Version   int        `json:"version"`
	IsDefault bool       `json:"is_default"`
	UpdatedAt *time.Time `json:"updated_at"`
}

// writeLayout answers with the layout, its preview and its check results.
// Compiled layouts are cached, so this rarely compiles.
func (s *Service) writeLayout(w http.ResponseWriter, r *http.Request, b store.Brand, l store.Layout, extra map[string]any) error {
	cc, err := checkContext(r.Context(), s.store, b)
	if err != nil {
		return err
	}
	preview, issues, err := LayoutPreview(r.Context(), l.MJML)
	if err != nil {
		return err
	}
	if len(issues) == 0 {
		issues = Check(preview, cc)
	}
	out := map[string]any{
		"layout":       layoutView{MJML: l.MJML, Version: l.Version, IsDefault: l.Version == 0, UpdatedAt: l.UpdatedAt},
		"preview_html": Render(preview, SampleVars(b.Name, b.PostalAddress)),
		"issues":       nonNil(issues),
		"tags":         Tags,
	}
	for k, v := range extra {
		out[k] = v
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (s *Service) getLayout(w http.ResponseWriter, r *http.Request) error {
	b, _, err := s.visibleBrand(r)
	if err != nil {
		return err
	}
	l, err := layoutFor(r.Context(), s.store, b)
	if err != nil {
		return err
	}
	return s.writeLayout(w, r, b, l, nil)
}

type layoutInput struct {
	MJML    string `json:"mjml"`
	Version int    `json:"version"` // the version being edited; 0 when editing the default
}

func (s *Service) previewLayout(w http.ResponseWriter, r *http.Request) error {
	b, _, err := s.brandWithRole(r, store.RoleBrandAdmin)
	if err != nil {
		return err
	}
	var in layoutInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	return s.writeLayout(w, r, b, store.Layout{MJML: in.MJML, Version: in.Version}, nil)
}

// changeContent runs a layout or allowlist change: a short transaction that
// makes the change, starts a new template generation and writes the audit
// entry, then a background rebuild of every template. Nothing compiles
// while the transaction is open.
func (s *Service) changeContent(ctx context.Context, b store.Brand, change func(tx *store.Store) error) (queued int, err error) {
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := change(tx); err != nil {
			return err
		}
		if _, err := tx.BumpTemplateGeneration(ctx, b.ID); err != nil {
			return err
		}
		queued, err = tx.CountLiveTemplates(ctx, b.ID)
		return err
	})
	if err == nil {
		s.queueRebuild(ctx, b.ID)
	}
	return queued, err
}

func (s *Service) putLayout(w http.ResponseWriter, r *http.Request, az authorizer) error {
	b, p, err := s.adminWithStepUp(r, az)
	if err != nil {
		return err
	}
	var in layoutInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	cc, err := checkContext(r.Context(), s.store, b)
	if err != nil {
		return err
	}
	issues, err := CheckLayout(r.Context(), in.MJML, cc)
	if err != nil {
		return err
	}
	if HasErrors(issues) {
		return rejected(w, "layout_has_errors", "the layout is used by every email this brand sends, so it must pass every check before it can be saved", issues)
	}

	// A second admin approves layout changes. Only a company that has never
	// had two admins may skip that, and it's recorded.
	ch := layoutChange{BrandID: b.ID, FromVersion: in.Version, MJML: in.MJML}
	var saved store.Layout
	var pending *store.Approval
	var queued int
	err = s.store.InTx(r.Context(), func(tx *store.Store) error {
		current, err := layoutFor(r.Context(), tx, b)
		if err != nil {
			return err
		}
		if current.Version != in.Version {
			return store.ErrStale
		}
		if s.approvals != nil {
			a, err := s.approvals.Request(r.Context(), tx, approvals.Request{
				Kind: approvals.KindLayoutChange, BrandID: &b.ID, RequestedBy: p.User,
				Summary: fmt.Sprintf("Change %s's locked layout (version %d → %d)", b.Name, in.Version, in.Version+1),
				Payload: ch, Audit: auth.AuditEntry(r, "", "", "", &b.ID),
			})
			switch {
			case err == nil:
				pending = &a
				return nil
			case !errors.Is(err, approvals.ErrNoApprover):
				return err
			}
			ok, err := s.approvals.ShortcutAllowed(r.Context(), tx)
			if err != nil {
				return err
			}
			if !ok {
				return errNoApprover
			}
		}
		if saved, err = s.applyLayout(r.Context(), tx, b, ch, p.User.ID, auth.AuditEntry(r, "", "", "", &b.ID), s.approvals != nil); err != nil {
			return err
		}
		queued, err = tx.CountLiveTemplates(r.Context(), b.ID)
		return err
	})
	switch {
	case errors.Is(err, store.ErrStale):
		return errStale
	case err != nil:
		return err
	case pending != nil:
		httpx.WriteJSON(w, http.StatusAccepted, map[string]any{
			"approval": map[string]any{"id": pending.ID, "status": pending.Status, "expires_at": pending.ExpiresAt},
		})
		return nil
	}
	s.queueRebuild(r.Context(), b.ID)
	return s.writeLayout(w, r, b, saved, map[string]any{"templates_queued": queued})
}

var errNoApprover = httpx.Errorf(http.StatusConflict, "no_approver",
	"layout changes need a second admin, and nobody else can approve right now; ask another admin to be unfrozen or invited")

// --- link allowlist ---

func (s *Service) getAllowlist(w http.ResponseWriter, r *http.Request) error {
	b, _, err := s.visibleBrand(r)
	if err != nil {
		return err
	}
	return s.writeAllowlist(w, r, b, http.StatusOK, nil)
}

func (s *Service) writeAllowlist(w http.ResponseWriter, r *http.Request, b store.Brand, status int, extra map[string]any) error {
	list, err := s.store.LinkAllowlist(r.Context(), b.ID)
	if err != nil {
		return err
	}
	out := map[string]any{"domains": nonNil(list), "brand_domains": []string{b.SendingDomain, b.LinkDomain}}
	for k, v := range extra {
		out[k] = v
	}
	httpx.WriteJSON(w, status, out)
	return nil
}

var domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

func (s *Service) allowDomain(w http.ResponseWriter, r *http.Request, az authorizer) error {
	b, p, err := s.adminWithStepUp(r, az)
	if err != nil {
		return err
	}
	var in struct {
		Domain string `json:"domain"`
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	d := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(in.Domain), "."))
	d = strings.TrimPrefix(strings.TrimPrefix(d, "https://"), "http://")
	suffix, _ := publicsuffix.PublicSuffix(d)
	switch {
	case !domainRe.MatchString(d):
		return bad("invalid_domain", "%q is not a domain name, e.g. shop.example.com", in.Domain)
	case IsShortener(d):
		return bad("shortener", "%s is a link shortener; shortened links are never allowed", d)
	case suffix == d:
		// co.uk or github.io would allow every site under it.
		return bad("public_suffix", "%s is shared by many unrelated sites; allow a specific domain under it", d)
	}

	queued, err := s.changeContent(r.Context(), b, func(tx *store.Store) error {
		if _, err := tx.AllowDomain(r.Context(), b.ID, d, p.User.ID); err != nil {
			return err
		}
		return tx.Audit(r.Context(), auth.AuditEntry(r, "link_allowlist.added", "link_allowlist", d, &b.ID), nil, map[string]string{"domain": d})
	})
	if errors.Is(err, store.ErrConflict) {
		return httpx.Errorf(http.StatusConflict, "already_allowed", "%s is already on the allowlist", d)
	}
	if err != nil {
		return err
	}
	return s.writeAllowlist(w, r, b, http.StatusCreated, map[string]any{"templates_queued": queued})
}

func (s *Service) disallowDomain(w http.ResponseWriter, r *http.Request, az authorizer) error {
	b, _, err := s.adminWithStepUp(r, az)
	if err != nil {
		return err
	}
	d := strings.ToLower(r.PathValue("domain"))
	queued, err := s.changeContent(r.Context(), b, func(tx *store.Store) error {
		if err := tx.DisallowDomain(r.Context(), b.ID, d); err != nil {
			return err
		}
		return tx.Audit(r.Context(), auth.AuditEntry(r, "link_allowlist.removed", "link_allowlist", d, &b.ID), map[string]string{"domain": d}, nil)
	})
	if errors.Is(err, store.ErrNotFound) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	return s.writeAllowlist(w, r, b, http.StatusOK, map[string]any{"templates_queued": queued})
}

// --- templates ---

func (s *Service) listTemplates(w http.ResponseWriter, r *http.Request) error {
	b, _, err := s.visibleBrand(r)
	if err != nil {
		return err
	}
	list, err := s.store.ListTemplates(r.Context(), b.ID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"templates": nonNil(list)})
	return nil
}

type templateInput struct {
	Name    string `json:"name"`
	MJML    string `json:"mjml"`
	Version int    `json:"version"` // on update: the version being edited
}

func (in *templateInput) normalize() error {
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case in.Name == "":
		return bad("invalid_name", "give the template a name")
	case len([]rune(in.Name)) > 200:
		return bad("invalid_name", "the name is too long (200 characters at most)")
	case strings.TrimSpace(in.MJML) == "":
		return bad("invalid_mjml", "the template is empty")
	}
	return nil
}

func (s *Service) templateID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.ErrNotFound
	}
	return id, nil
}

// writeTemplate answers with the template and a preview filled with sample data.
func (s *Service) writeTemplate(w http.ResponseWriter, status int, b store.Brand, t store.Template) error {
	vars := SampleVars(b.Name, b.PostalAddress)
	httpx.WriteJSON(w, status, map[string]any{
		"template":     t,
		"preview_html": Render(t.HTML, vars),
		"preview_text": RenderText(t.TextBody, vars),
		"tags":         Tags,
	})
	return nil
}

func (s *Service) getTemplate(w http.ResponseWriter, r *http.Request) error {
	b, _, err := s.visibleBrand(r)
	if err != nil {
		return err
	}
	id, err := s.templateID(r)
	if err != nil {
		return err
	}
	t, err := s.store.TemplateByID(r.Context(), b.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	return s.writeTemplate(w, http.StatusOK, b, t)
}

func (s *Service) previewTemplate(w http.ResponseWriter, r *http.Request) error {
	b, _, err := s.brandWithRole(r, store.RoleEditor)
	if err != nil {
		return err
	}
	var in templateInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	bs, err := readState(r.Context(), s.store, b)
	if err != nil {
		return err
	}
	res, issues, err := build(r.Context(), bs, in.MJML)
	if err != nil {
		return err
	}
	vars := SampleVars(b.Name, b.PostalAddress)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"preview_html": Render(res.HTML, vars),
		"preview_text": RenderText(res.Text, vars),
		"issues":       issues,
	})
	return nil
}

var errLayoutChanging = httpx.Errorf(http.StatusConflict, "layout_changing", "the brand layout or allowlist is changing right now; save again in a moment")

// saveTemplate compiles a template with no lock held, then stores it if the
// layout and allowlist it was built against are still current; otherwise it
// compiles again against the new ones.
func (s *Service) saveTemplate(r *http.Request, b store.Brand, mjml string, save func(tx *store.Store, res store.TemplateBuild) (store.Template, error)) (store.Template, error) {
	for range 3 {
		bs, err := readState(r.Context(), s.store, b)
		if err != nil {
			return store.Template{}, err
		}
		res, _, err := build(r.Context(), bs, mjml)
		if err != nil {
			return store.Template{}, err
		}
		var t store.Template
		var moved bool
		err = s.store.InTx(r.Context(), func(tx *store.Store) error {
			g, err := tx.LockTemplateGeneration(r.Context(), b.ID)
			if err != nil {
				return err
			}
			if g != bs.generation {
				moved = true
				return nil
			}
			t, err = save(tx, res)
			return err
		})
		switch {
		case errors.Is(err, store.ErrConflict):
			return t, httpx.Errorf(http.StatusConflict, "template_name_taken", "another template of this brand already has that name")
		case errors.Is(err, store.ErrStale):
			return t, errStale
		case errors.Is(err, store.ErrNotFound):
			return t, httpx.ErrNotFound
		case err != nil || !moved:
			return t, err
		}
	}
	return store.Template{}, errLayoutChanging
}

func (s *Service) createTemplate(w http.ResponseWriter, r *http.Request) error {
	b, p, err := s.brandWithRole(r, store.RoleEditor)
	if err != nil {
		return err
	}
	var in templateInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	if err := in.normalize(); err != nil {
		return err
	}
	t, err := s.saveTemplate(r, b, in.MJML, func(tx *store.Store, res store.TemplateBuild) (store.Template, error) {
		t, err := tx.CreateTemplate(r.Context(), b.ID, in.Name, in.MJML, res, p.User.ID)
		if err != nil {
			return t, err
		}
		return t, tx.Audit(r.Context(), auth.AuditEntry(r, "template.created", "template", t.ID.String(), &b.ID),
			nil, auditTemplate(t.ID, t.Name, t.MJML, res))
	})
	if err != nil {
		return err
	}
	return s.writeTemplate(w, http.StatusCreated, b, t)
}

func (s *Service) updateTemplate(w http.ResponseWriter, r *http.Request) error {
	b, p, err := s.brandWithRole(r, store.RoleEditor)
	if err != nil {
		return err
	}
	id, err := s.templateID(r)
	if err != nil {
		return err
	}
	var in templateInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	if err := in.normalize(); err != nil {
		return err
	}
	t, err := s.saveTemplate(r, b, in.MJML, func(tx *store.Store, res store.TemplateBuild) (store.Template, error) {
		before, err := tx.TemplateByID(r.Context(), b.ID, id)
		if err != nil {
			return store.Template{}, err
		}
		t, err := tx.UpdateTemplate(r.Context(), b.ID, id, in.Name, in.MJML, res, in.Version, p.User.ID)
		if err != nil {
			return t, err
		}
		prev := store.TemplateBuild{ErrorCount: before.ErrorCount, WarningCount: before.WarningCount, LayoutVersion: before.LayoutVersion}
		return t, tx.Audit(r.Context(), auth.AuditEntry(r, "template.updated", "template", t.ID.String(), &b.ID),
			auditTemplate(before.ID, before.Name, before.MJML, prev), auditTemplate(t.ID, t.Name, t.MJML, res))
	})
	if err != nil {
		return err
	}
	return s.writeTemplate(w, http.StatusOK, b, t)
}

func (s *Service) archiveTemplate(w http.ResponseWriter, r *http.Request) error {
	b, _, err := s.brandWithRole(r, store.RoleEditor)
	if err != nil {
		return err
	}
	id, err := s.templateID(r)
	if err != nil {
		return err
	}
	err = s.store.InTx(r.Context(), func(tx *store.Store) error {
		t, err := tx.TemplateByID(r.Context(), b.ID, id)
		if err != nil {
			return err
		}
		if err := tx.ArchiveTemplate(r.Context(), b.ID, id); err != nil {
			return err
		}
		return tx.Audit(r.Context(), auth.AuditEntry(r, "template.archived", "template", id.String(), &b.ID), map[string]any{"id": id, "name": t.Name}, nil)
	})
	if errors.Is(err, store.ErrNotFound) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

package unsubscribe

import (
	"embed"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/cache"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
)

//go:embed pages.html
var pagesFS embed.FS

var pages = template.Must(template.New("pages.html").Funcs(template.FuncMap{
	"mask": maskEmail,
	"initial": func(s string) string {
		r := []rune(s)
		if len(r) == 0 {
			return ""
		}
		return string(r[0])
	},
}).ParseFS(pagesFS, "pages.html"))

// Rate limits for the public POSTs (no login, no CSRF by design), per minute.
// One-click POSTs come from mail providers' shared servers (one IP for many
// people), so they're limited per contact only; dropping one loses an unsubscribe.
const (
	perContactLimit = 10
	perIPPageLimit  = 120 // human page POSTs only
	maxFormBytes    = 64 << 10
)

type Handlers struct {
	Signer  *Signer
	Service *Service
	Limiter cache.Limiter
	Log     *slog.Logger
}

// Routes registers the public pages at the server root (served on each
// brand's link domain). Deliberately outside /admin: no session, no CORS.
func (h *Handlers) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+PagePath+"{token}", h.unsubscribePage)
	mux.HandleFunc("POST "+PagePath+"{token}", h.unsubscribePost)
	mux.HandleFunc("GET "+PreferencesPath+"{token}", h.preferencesPage)
	mux.HandleFunc("POST "+PreferencesPath+"{token}", h.preferencesPost)
}

type pageData struct {
	View
	Page       string // confirm | already | done | preferences | invalid | busy | error
	Token      string
	Scope      string // done: "topic" or "all"
	Saved      bool
	CantChange bool
	Year       int
}

func securityHeaders(w http.ResponseWriter) {
	h := w.Header()
	// The token in the URL is a credential: never cache it, leak it or frame it.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("X-Content-Type-Options", "nosniff")
}

func (h *Handlers) render(w http.ResponseWriter, r *http.Request, status int, d pageData) {
	securityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	d.Year = time.Now().Year()
	if err := pages.ExecuteTemplate(w, "page", d); err != nil {
		h.Log.ErrorContext(r.Context(), "rendering public page", "page", d.Page, "err", err)
	}
}

// invalid is the same for malformed, tampered and unknown-brand tokens: it
// says nothing about whether anyone exists.
func (h *Handlers) invalid(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusNotFound, pageData{Page: "invalid"})
}

func (h *Handlers) failed(w http.ResponseWriter, r *http.Request, err error) {
	h.Log.ErrorContext(r.Context(), "public unsubscribe page", "err", err)
	h.render(w, r, http.StatusInternalServerError, pageData{Page: "error"})
}

func (h *Handlers) claims(w http.ResponseWriter, r *http.Request) (Claims, string, bool) {
	tok := r.PathValue("token")
	c, err := h.Signer.Verify(tok)
	if err != nil {
		h.invalid(w, r)
		return c, tok, false
	}
	return c, tok, true
}

// limited applies the rate limits to a POST. The contact key comes from the
// verified token, so re-encoding a token can't dodge it.
func (h *Handlers) limited(r *http.Request, c Claims, oneClick bool) bool {
	type rule struct {
		key   string
		limit int
	}
	rules := []rule{{"unsub:contact:" + c.BrandID.String() + ":" + c.ContactID.String(), perContactLimit}}
	if !oneClick {
		rules = append(rules, rule{"unsub:ip:" + httpx.ClientIP(r), perIPPageLimit})
	}
	for _, l := range rules {
		ok, err := h.Limiter.Allow(r.Context(), l.key, l.limit, time.Minute)
		if err != nil {
			h.Log.WarnContext(r.Context(), "rate limiter unavailable; allowing", "err", err)
		}
		if !ok {
			return true
		}
	}
	return false
}

func (h *Handlers) load(w http.ResponseWriter, r *http.Request, c Claims) (View, bool) {
	v, err := h.Service.Load(r.Context(), c)
	if errors.Is(err, errUnknownBrand) {
		h.invalid(w, r)
		return v, false
	}
	if err != nil {
		h.failed(w, r, err)
		return v, false
	}
	return v, true
}

// unsubscribePage (P4) only shows a confirm button. GETs never change
// anything: mail scanners open every link in an email.
func (h *Handlers) unsubscribePage(w http.ResponseWriter, r *http.Request) {
	c, tok, ok := h.claims(w, r)
	if !ok {
		return
	}
	v, ok := h.load(w, r, c)
	if !ok {
		return
	}
	page := "confirm"
	if v.Unsubscribed() || (v.Topic != nil && !v.IsSubscribed(v.Topic.ID)) {
		page = "already"
	}
	h.render(w, r, http.StatusOK, pageData{View: v, Page: page, Token: tok})
}

// unsubscribePost handles both the page's confirm button and the RFC 8058
// one-click POST that Gmail, Yahoo and Apple Mail send to the List-Unsubscribe URL.
func (h *Handlers) unsubscribePost(w http.ResponseWriter, r *http.Request) {
	c, tok, ok := h.claims(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		_ = r.ParseMultipartForm(maxFormBytes)
	} else {
		_ = r.ParseForm()
	}
	oneClick := r.PostFormValue("List-Unsubscribe") == "One-Click"
	if h.limited(r, c, oneClick) {
		if oneClick {
			securityHeaders(w)
			http.Error(w, "too many requests", http.StatusTooManyRequests)
		} else {
			h.render(w, r, http.StatusTooManyRequests, pageData{Page: "busy"})
		}
		return
	}

	if oneClick {
		_, err := h.Service.Unsubscribe(r.Context(), c, false, Request{Method: "header", IP: httpx.ClientIP(r)})
		securityHeaders(w)
		switch {
		case errors.Is(err, errUnknownBrand):
			http.Error(w, "unknown link", http.StatusNotFound)
		case err != nil:
			h.Log.ErrorContext(r.Context(), "one-click unsubscribe", "err", err)
			http.Error(w, "please try again", http.StatusInternalServerError)
		default:
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("Unsubscribed.\n"))
		}
		return
	}

	all := r.PostFormValue("scope") == "all"
	v, err := h.Service.Unsubscribe(r.Context(), c, all, Request{Method: "link", IP: httpx.ClientIP(r)})
	if errors.Is(err, errUnknownBrand) {
		h.invalid(w, r)
		return
	}
	if err != nil {
		h.failed(w, r, err)
		return
	}
	scope := "all"
	if !all && v.Topic != nil && len(v.Subscribed) > 0 {
		scope = "topic"
	}
	h.render(w, r, http.StatusOK, pageData{View: v, Page: "done", Token: tok, Scope: scope})
}

// preferencesPage (P3) shows the topics with checkboxes. Read-only.
func (h *Handlers) preferencesPage(w http.ResponseWriter, r *http.Request) {
	c, tok, ok := h.claims(w, r)
	if !ok {
		return
	}
	v, ok := h.load(w, r, c)
	if !ok {
		return
	}
	h.render(w, r, http.StatusOK, pageData{View: v, Page: "preferences", Token: tok})
}

func (h *Handlers) preferencesPost(w http.ResponseWriter, r *http.Request) {
	c, tok, ok := h.claims(w, r)
	if !ok {
		return
	}
	if h.limited(r, c, false) {
		h.render(w, r, http.StatusTooManyRequests, pageData{Page: "busy"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		h.render(w, r, http.StatusBadRequest, pageData{Page: "invalid"})
		return
	}
	req := Request{Method: "link", IP: httpx.ClientIP(r)}

	var v View
	var err error
	if r.PostFormValue("action") == "unsubscribe_all" {
		v, err = h.Service.Unsubscribe(r.Context(), c, true, req)
	} else {
		var want []uuid.UUID
		for _, s := range r.PostForm["topic"] {
			if id, perr := uuid.Parse(s); perr == nil {
				want = append(want, id)
			}
		}
		v, err = h.Service.SavePreferences(r.Context(), c, want, req)
	}
	switch {
	case errors.Is(err, errUnknownBrand):
		h.invalid(w, r)
	case errors.Is(err, ErrCantSubscribe):
		v, _ = h.Service.Load(r.Context(), c)
		h.render(w, r, http.StatusConflict, pageData{View: v, Page: "preferences", Token: tok, CantChange: true})
	case err != nil:
		h.failed(w, r, err)
	default:
		h.render(w, r, http.StatusOK, pageData{View: v, Page: "preferences", Token: tok, Saved: true})
	}
}

// maskEmail shows enough to recognise the address without spelling it out
// to anyone the email was forwarded to: m•••@gmail.com.
func maskEmail(email string) string {
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" {
		return "•••"
	}
	r := []rune(local)
	return string(r[0]) + "•••@" + domain
}

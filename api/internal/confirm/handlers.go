package confirm

import (
	"embed"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kmrifat/klavio-alternative/api/internal/cache"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

//go:embed pages.html
var pagesFS embed.FS

var pages = template.Must(template.New("pages.html").Funcs(template.FuncMap{"mask": maskEmail}).ParseFS(pagesFS, "pages.html"))

// Rate limits for the public confirm POST, per minute.
const (
	perIPLimit      = 60
	perContactLimit = 10
)

type Handlers struct {
	Signer  *Signer
	Service *Service
	Limiter cache.Limiter
	Log     *slog.Logger
}

// Routes: the confirm page (P1) on each brand's link domain. GET only shows
// a button, so mail scanners that open links can't subscribe anyone; the
// POST confirms.
func (h *Handlers) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+Path+"{token}", h.page)
	mux.HandleFunc("POST "+Path+"{token}", h.post)
}

type pageData struct {
	View
	Page string // ask | confirmed | already | cant | expired | invalid | busy | error
}

func (h *Handlers) render(w http.ResponseWriter, r *http.Request, status int, d pageData) {
	hd := w.Header()
	hd.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Robots-Tag", "noindex, nofollow")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := pages.ExecuteTemplate(w, "page", d); err != nil {
		h.Log.ErrorContext(r.Context(), "rendering confirm page", "page", d.Page, "err", err)
	}
}

// claims verifies the token; on failure it has already answered.
func (h *Handlers) claims(w http.ResponseWriter, r *http.Request) (Claims, bool) {
	c, err := h.Signer.Verify(r.PathValue("token"))
	switch {
	case errors.Is(err, ErrExpired):
		h.render(w, r, http.StatusGone, pageData{Page: "expired"})
		return c, false
	case err != nil:
		h.render(w, r, http.StatusNotFound, pageData{Page: "invalid"})
		return c, false
	}
	return c, true
}

func (h *Handlers) page(w http.ResponseWriter, r *http.Request) {
	c, ok := h.claims(w, r)
	if !ok {
		return
	}
	v, err := h.Service.Load(r.Context(), c)
	switch {
	case errors.Is(err, store.ErrNotFound):
		h.render(w, r, http.StatusNotFound, pageData{Page: "invalid"})
		return
	case err != nil:
		h.Log.ErrorContext(r.Context(), "confirm page", "err", err)
		h.render(w, r, http.StatusInternalServerError, pageData{Page: "error"})
		return
	}
	page := "ask"
	switch {
	case v.Contact == nil || v.Contact.SuppressionReason != nil:
		page = "cant"
	case v.Contact.Status == store.ContactSubscribed:
		page = "already"
	}
	h.render(w, r, http.StatusOK, pageData{View: v, Page: page})
}

func (h *Handlers) post(w http.ResponseWriter, r *http.Request) {
	c, ok := h.claims(w, r)
	if !ok {
		return
	}
	for key, limit := range map[string]int{
		"confirm:ip:" + httpx.ClientIP(r):         perIPLimit,
		"confirm:contact:" + c.ContactID.String(): perContactLimit,
	} {
		allowed, err := h.Limiter.Allow(r.Context(), key, limit, time.Minute)
		if err != nil {
			h.Log.WarnContext(r.Context(), "confirm rate limiter", "err", err)
			continue // fail open: confirming is harmless
		}
		if !allowed {
			h.render(w, r, http.StatusTooManyRequests, pageData{Page: "busy"})
			return
		}
	}
	v, out, err := h.Service.Confirm(r.Context(), c, httpx.ClientIP(r))
	switch {
	case errors.Is(err, store.ErrNotFound):
		h.render(w, r, http.StatusNotFound, pageData{Page: "invalid"})
		return
	case err != nil:
		h.Log.ErrorContext(r.Context(), "confirm", "err", err)
		h.render(w, r, http.StatusInternalServerError, pageData{Page: "error"})
		return
	}
	h.render(w, r, http.StatusOK, pageData{View: v, Page: string(out)})
}

// maskEmail: a•••@example.com — the page shouldn't print a full address to
// whoever holds the link.
func maskEmail(e string) string {
	local, domain, ok := strings.Cut(e, "@")
	if !ok || local == "" {
		return "•••"
	}
	return string([]rune(local)[0]) + "•••@" + domain
}

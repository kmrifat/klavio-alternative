// Package server is the HTTP layer: routing, middleware and handlers.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
)

// Pinger reports whether a dependency (Postgres, Redis) is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

type Options struct {
	Log            *slog.Logger
	DB             Pinger
	Redis          Pinger
	Version        string
	Env            string
	Debug          bool // shown in /admin/v1/meta, so the app can offer debug-only links
	AllowedOrigins []string
	// Proxy decides whose Cloudflare headers (client IP, country) to believe.
	Proxy httpx.ProxyTrust

	// RegisterAdmin adds the feature routes under /admin/v1.
	RegisterAdmin func(mux *http.ServeMux)
	// AdminMiddleware wraps every /admin request (authentication).
	AdminMiddleware func(http.Handler) http.Handler
	// RegisterPublic adds unauthenticated routes at the root (unsubscribe pages).
	RegisterPublic func(mux *http.ServeMux)
	// CompanyName is shown in /admin/v1/meta, so the sign-in page can show it.
	CompanyName func(ctx context.Context) string
}

// New returns the root handler with all routes and middleware.
func New(o Options) http.Handler {
	mux := http.NewServeMux()

	// Probes: no auth, no CORS. /healthz = process alive, /readyz = can serve traffic.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		status, body := http.StatusOK, map[string]string{"status": "ok", "database": "up", "redis": "up"}
		for name, dep := range map[string]Pinger{"database": o.DB, "redis": o.Redis} {
			if err := dep.Ping(ctx); err != nil {
				o.Log.WarnContext(ctx, "readiness check failed", "dependency", name, "err", err)
				status, body["status"], body[name] = http.StatusServiceUnavailable, "unavailable", "down"
			}
		}
		writeJSON(w, status, body)
	})

	// Admin API, used by the React app. Behind Cloudflare Access in production.
	var adminH http.Handler = AdminMux(o)
	if o.AdminMiddleware != nil {
		adminH = o.AdminMiddleware(adminH)
	}
	mux.Handle("/admin/", cors(o.AllowedOrigins, csrf(o.AllowedOrigins, adminH)))
	if o.RegisterPublic != nil {
		o.RegisterPublic(mux)
	}

	return recoverer(o.Log, o.Proxy.Middleware(o.Log, requestLog(o.Log, mux)))
}

// AdminMux holds every /admin/v1 route, before middleware. New serves it;
// tests read its routing to check the API reference covers it.
func AdminMux(o Options) *http.ServeMux {
	admin := http.NewServeMux()
	admin.HandleFunc("GET /admin/v1/meta", func(w http.ResponseWriter, r *http.Request) {
		meta := map[string]any{"version": o.Version, "env": o.Env, "debug": o.Debug, "company_name": ""}
		if o.CompanyName != nil {
			meta["company_name"] = o.CompanyName(r.Context())
		}
		writeJSON(w, http.StatusOK, meta)
	})
	if o.RegisterAdmin != nil {
		o.RegisterAdmin(admin)
	}
	return admin
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// cors allows credentialed requests from the listed origins only.
func cors(allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && slices.Contains(allowed, origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
				h.Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// csrf blocks cross-site writes. Browsers can't send a JSON content type
// cross-site without a CORS preflight, which cors() only allows for our origins;
// the Origin check is a second line of defence.
func csrf(allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" && !slices.Contains(allowed, origin) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{"code": "bad_origin", "message": "request from an unknown origin"}})
			return
		}
		ct := r.Header.Get("Content-Type")
		// File uploads can't be JSON; a browser always sends Origin with them.
		if strings.HasPrefix(ct, "multipart/form-data") && origin != "" {
			next.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(ct, "application/json") {
			writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"error": map[string]string{"code": "json_required", "message": "send Content-Type: application/json"}})
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func requestLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" || len(id) > 64 {
			id = newRequestID()
		}
		w.Header().Set("X-Request-ID", id)

		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		// Probes are hit every few seconds; keep them out of the info log.
		level := slog.LevelInfo
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			level = slog.LevelDebug
		}
		log.Log(r.Context(), level, "http request",
			"id", id, "method", r.Method, "path", redactPath(r.URL.Path),
			"status", rec.status, "duration_ms", time.Since(start).Milliseconds())
	})
}

// redactPath hides what mustn't reach the logs: the signed token in public
// link paths (/u/, /p/, /confirm/; click /c/ and open /o/ tokens name the
// person too), which works like a password for that person's
// subscription; invite tokens, which let someone join; and the email
// address in Contacts API paths. Extra slashes ("//u/…", which the router
// redirects) don't get past it.
func redactPath(p string) string {
	if len(p) > 1 && p[1] == '/' {
		p = "/" + strings.TrimLeft(p, "/")
	}
	for _, prefix := range []string{"/u/", "/p/", "/confirm/", "/c/", "/o/"} {
		if strings.HasPrefix(p, prefix) && len(p) > len(prefix) {
			return prefix + "[token]"
		}
	}
	const invites = "/admin/v1/invites/"
	if rest, ok := strings.CutPrefix(p, invites); ok && rest != "" {
		if _, action, found := strings.Cut(rest, "/"); found {
			return invites + "[token]/" + action
		}
		return invites + "[token]"
	}
	const contacts = "/api/v1/contacts/"
	if rest, ok := strings.CutPrefix(p, contacts); ok && rest != "" {
		if _, action, found := strings.Cut(rest, "/"); found {
			return contacts + "[email]/" + action
		}
		return contacts + "[email]"
	}
	return p
}

func recoverer(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				log.ErrorContext(r.Context(), "panic in handler", "panic", v, "path", redactPath(r.URL.Path))
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

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
	AllowedOrigins []string

	// RegisterAdmin adds the feature routes under /admin/v1.
	RegisterAdmin func(mux *http.ServeMux)
	// AdminMiddleware wraps every /admin request (authentication).
	AdminMiddleware func(http.Handler) http.Handler
	// RegisterPublic adds unauthenticated routes at the root (unsubscribe pages).
	RegisterPublic func(mux *http.ServeMux)
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
	admin := http.NewServeMux()
	admin.HandleFunc("GET /admin/v1/meta", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": o.Version, "env": o.Env})
	})
	if o.RegisterAdmin != nil {
		o.RegisterAdmin(admin)
	}
	var adminH http.Handler = admin
	if o.AdminMiddleware != nil {
		adminH = o.AdminMiddleware(adminH)
	}
	mux.Handle("/admin/", cors(o.AllowedOrigins, csrf(o.AllowedOrigins, adminH)))
	if o.RegisterPublic != nil {
		o.RegisterPublic(mux)
	}

	return recoverer(o.Log, requestLog(o.Log, mux))
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

// redactPath hides the signed token in public link paths (/u/<token>,
// /p/<token>): it works like a password for that person's subscription.
func redactPath(p string) string {
	for _, prefix := range []string{"/u/", "/p/"} {
		if strings.HasPrefix(p, prefix) && len(p) > len(prefix) {
			return prefix + "[token]"
		}
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
				log.ErrorContext(r.Context(), "panic in handler", "panic", v, "path", r.URL.Path)
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

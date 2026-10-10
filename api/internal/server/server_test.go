package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

func newTestServer(dbErr error) http.Handler {
	return newTestServerDeps(dbErr, nil)
}

func newTestServerDeps(dbErr, redisErr error) http.Handler {
	return New(Options{
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:             fakeDB{err: dbErr},
		Redis:          fakeDB{err: redisErr},
		Version:        "test",
		Env:            "development",
		AllowedOrigins: []string{"http://localhost:5173"},
	})
}

func do(t *testing.T, h http.Handler, req *http.Request) (*httptest.ResponseRecorder, map[string]string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body map[string]string
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body %q: %v", rec.Body.String(), err)
		}
	}
	return rec, body
}

func TestHealthz(t *testing.T) {
	rec, body := do(t, newTestServer(nil), httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("got %d %v", rec.Code, body)
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("missing X-Request-ID header")
	}
}

func TestReadyz(t *testing.T) {
	down := errors.New("conn refused")
	cases := []struct {
		name            string
		dbErr, redisErr error
		code            int
		database, redis string
	}{
		{"all up", nil, nil, http.StatusOK, "up", "up"},
		{"db down", down, nil, http.StatusServiceUnavailable, "down", "up"},
		{"redis down", nil, down, http.StatusServiceUnavailable, "up", "down"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, body := do(t, newTestServerDeps(c.dbErr, c.redisErr), httptest.NewRequest("GET", "/readyz", nil))
			if rec.Code != c.code || body["database"] != c.database || body["redis"] != c.redis {
				t.Fatalf("got %d %v", rec.Code, body)
			}
		})
	}
}

func TestMeta(t *testing.T) {
	rec, body := do(t, newTestServer(nil), httptest.NewRequest("GET", "/admin/v1/meta", nil))
	if rec.Code != http.StatusOK || body["version"] != "test" || body["env"] != "development" {
		t.Fatalf("got %d %v", rec.Code, body)
	}
}

func TestCORS(t *testing.T) {
	h := newTestServer(nil)

	req := httptest.NewRequest("OPTIONS", "/admin/v1/meta", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec, _ := do(t, h, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Errorf("allow-origin = %q", got)
	}

	req = httptest.NewRequest("GET", "/admin/v1/meta", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec, _ = do(t, h, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("unknown origin was allowed: %q", got)
	}
}

func TestPanicIsRecovered(t *testing.T) {
	h := recoverer(slog.New(slog.NewTextHandler(io.Discard, nil)), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec, body := do(t, h, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusInternalServerError || body["error"] != "internal error" {
		t.Fatalf("got %d %v", rec.Code, body)
	}
}

func TestCSRF(t *testing.T) {
	h := New(Options{
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:             fakeDB{},
		Redis:          fakeDB{},
		AllowedOrigins: []string{"http://localhost:5173"},
		RegisterAdmin: func(mux *http.ServeMux) {
			mux.HandleFunc("POST /admin/v1/thing", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
		},
	})
	cases := []struct {
		name, origin, contentType string
		want                      int
	}{
		{"json from app", "http://localhost:5173", "application/json", http.StatusNoContent},
		{"json without origin (curl)", "", "application/json; charset=utf-8", http.StatusNoContent},
		{"form post", "http://localhost:5173", "application/x-www-form-urlencoded", http.StatusUnsupportedMediaType},
		{"other site", "https://evil.example", "application/json", http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/admin/v1/thing", strings.NewReader("{}"))
			if c.origin != "" {
				req.Header.Set("Origin", c.origin)
			}
			req.Header.Set("Content-Type", c.contentType)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d", rec.Code, c.want)
			}
		})
	}
}

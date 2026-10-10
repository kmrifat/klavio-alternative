package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

func newTestServer(dbErr error) http.Handler {
	return New(Options{
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:             fakeDB{err: dbErr},
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
	rec, body := do(t, newTestServer(nil), httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusOK || body["database"] != "up" {
		t.Fatalf("db up: got %d %v", rec.Code, body)
	}

	rec, body = do(t, newTestServer(errors.New("conn refused")), httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable || body["database"] != "down" {
		t.Fatalf("db down: got %d %v", rec.Code, body)
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

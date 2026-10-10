// Package httpx has small helpers shared by HTTP handlers: JSON in/out and
// typed errors that map to status codes.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

// Error is an error with an HTTP status and a stable machine-readable code.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func Errorf(status int, code, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

var (
	ErrUnauthenticated = &Error{Status: http.StatusUnauthorized, Code: "unauthenticated", Message: "sign in first"}
	ErrForbidden       = &Error{Status: http.StatusForbidden, Code: "forbidden", Message: "you don't have permission to do this"}
	ErrStepUpRequired  = &Error{Status: http.StatusForbidden, Code: "step_up_required", Message: "confirm with your passkey first"}
	ErrNotFound        = &Error{Status: http.StatusNotFound, Code: "not_found", Message: "not found"}
)

// HandlerFunc is an http handler that returns an error instead of writing one.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// Handle adapts h; *Error values become JSON error bodies, anything else is a 500.
func Handle(log *slog.Logger, h HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := h(w, r)
		if err == nil {
			return
		}
		var he *Error
		if errors.As(err, &he) {
			WriteJSON(w, he.Status, map[string]*Error{"error": he})
			return
		}
		log.ErrorContext(r.Context(), "handler error", "err", err, "method", r.Method, "path", r.URL.Path)
		WriteJSON(w, http.StatusInternalServerError, map[string]*Error{"error": {Code: "internal", Message: "something went wrong"}})
	}
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ReadJSON decodes a JSON body (max 1 MB) and rejects unknown fields.
func ReadJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return Errorf(http.StatusBadRequest, "bad_json", "invalid request body: %v", err)
	}
	return nil
}

// ClientIP prefers Cloudflare's header (the tunnel is the only way in), then the socket address.
func ClientIP(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

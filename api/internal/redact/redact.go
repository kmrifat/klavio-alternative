// Package redact keeps email addresses out of the logs. Logs get copied to
// other tools and kept longer than the data, so a log line shows
// m•••@gmail.com, never the full address, wherever it came from: the
// message, an attribute, a request path or an error.
package redact

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

var email = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,}`)

// Emails masks every address in s: the first character of the local part
// stays, so a person reading the log can still tell addresses apart.
func Emails(s string) string {
	if !strings.Contains(s, "@") {
		return s
	}
	return email.ReplaceAllStringFunc(s, func(m string) string {
		return m[:1] + "•••" + m[strings.LastIndexByte(m, '@'):]
	})
}

// Handler wraps another slog handler and masks addresses in every record.
type Handler struct{ next slog.Handler }

func NewHandler(next slog.Handler) *Handler { return &Handler{next: next} }

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, Emails(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(attr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h *Handler) WithAttrs(as []slog.Attr) slog.Handler {
	masked := make([]slog.Attr, len(as))
	for i, a := range as {
		masked[i] = attr(a)
	}
	return &Handler{next: h.next.WithAttrs(masked)}
}

func (h *Handler) WithGroup(name string) slog.Handler { return &Handler{next: h.next.WithGroup(name)} }

func attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, Emails(v.String()))
	case slog.KindGroup:
		g := v.Group()
		masked := make([]any, len(g))
		for i, x := range g {
			masked[i] = attr(x)
		}
		return slog.Group(a.Key, masked...)
	case slog.KindAny:
		// Errors and anything that prints itself; other values (numbers,
		// structs logged as JSON) are left as they are.
		switch x := v.Any().(type) {
		case error:
			if s := x.Error(); strings.Contains(s, "@") {
				return slog.String(a.Key, Emails(s))
			}
		case fmt.Stringer:
			if s := x.String(); strings.Contains(s, "@") {
				return slog.String(a.Key, Emails(s))
			}
		case []string:
			masked := make([]string, len(x))
			for i, s := range x {
				masked[i] = Emails(s)
			}
			return slog.Any(a.Key, masked)
		}
	}
	return slog.Attr{Key: a.Key, Value: v}
}

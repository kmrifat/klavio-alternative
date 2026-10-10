// Package mailer is the seam between features that send one email at a
// time (double opt-in confirmations, re-permission requests) and whatever
// actually sends. The SES sender arrives with the sending worker (M3 part
// 3); until then development logs messages and production refuses.
package mailer

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// Message is one email from a brand to one person.
type Message struct {
	Brand   store.Brand
	To      string
	Subject string
	HTML    string
	Text    string
	// Headers, e.g. List-Unsubscribe for re-permission emails.
	Headers map[string]string
	// Kind tags the email for reporting: confirmation | repermission.
	Kind string
}

type Sender interface {
	Send(ctx context.Context, m Message) error
}

// ErrNotConfigured: no real sender yet.
var ErrNotConfigured = errors.New("mailer: no email sender is configured")

// Unconfigured refuses every message (production until SES sending lands).
type Unconfigured struct{}

func (Unconfigured) Send(context.Context, Message) error { return ErrNotConfigured }

// Log writes each message to the log instead of sending it. Development
// only: the body holds a confirmation link, which is a credential.
type Log struct{ Log *slog.Logger }

func (l Log) Send(ctx context.Context, m Message) error {
	l.Log.InfoContext(ctx, "email (development: not sent)", "kind", m.Kind, "brand", m.Brand.Slug, "to", m.To, "subject", m.Subject, "text", m.Text)
	return nil
}

// Memory keeps messages for tests.
type Memory struct {
	mu   sync.Mutex
	sent []Message
}

func (m *Memory) Send(_ context.Context, msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

func (m *Memory) Sent() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Message(nil), m.sent...)
}

package auth

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// An agent never passes a passkey check, even if a confirmation time were
// somehow attached, and its audit entries name the token.
func TestAgentPrincipal(t *testing.T) {
	s := &Service{now: time.Now}
	now := time.Now()
	tok := &store.AgentToken{Name: "Claude Code", Prefix: "kag_abc"}
	p := &Principal{User: store.User{Email: "olivia@acme.test"}, SteppedUpAt: &now, Agent: tok}
	r := httptest.NewRequest("POST", "/admin/v1/agent-tokens", nil)
	r = r.WithContext(WithPrincipal(r.Context(), p))

	if _, err := s.RequireStepUp(r); !errors.Is(err, ErrAgentNotAllowed) {
		t.Fatalf("agent step-up: %v", err)
	}
	if e := AuditEntry(r, "x", "", "", nil); e.ViaAgent != "Claude Code (kag_abc)" || e.ActorEmail != "olivia@acme.test" {
		t.Fatalf("audit entry: %+v", e)
	}

	p.Agent = nil // the same person, in the console
	if _, err := s.RequireStepUp(r); err != nil {
		t.Fatalf("person step-up: %v", err)
	}
	if e := AuditEntry(r, "x", "", "", nil); e.ViaAgent != "" {
		t.Fatalf("person's entry marked as agent: %+v", e)
	}
}

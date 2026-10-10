package confirm

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/unsubscribe"
)

const (
	secretA = "confirm-test-secret-aaaaaaaaaaaaaaaaaaaa"
	secretB = "confirm-test-secret-bbbbbbbbbbbbbbbbbbbb"
)

func TestTokens(t *testing.T) {
	s, err := NewSigner(secretA)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	c := Claims{BrandID: uuid.New(), ContactID: uuid.New(), Purpose: PurposeRepermission, IssuedAt: now}
	tok := s.Token(c)
	got, err := s.Verify(tok)
	if err != nil || got.BrandID != c.BrandID || got.ContactID != c.ContactID || got.Purpose != PurposeRepermission || !got.IssuedAt.Equal(now) {
		t.Fatalf("round trip: %+v %v", got, err)
	}

	// Tampering: flip the purpose byte (re-permission → opt-in).
	raw, _ := b64.DecodeString(tok)
	raw[2] = byte(PurposeOptIn)
	if _, err := s.Verify(b64.EncodeToString(raw)); !errors.Is(err, ErrSignature) {
		t.Fatalf("changed purpose: %v", err)
	}
	if _, err := s.Verify(tok + "A"); !errors.Is(err, ErrMalformed) {
		t.Fatalf("extra char: %v", err)
	}

	// Expiry.
	s.now = func() time.Time { return now.Add(TTL + time.Minute) }
	if _, err := s.Verify(tok); !errors.Is(err, ErrExpired) {
		t.Fatalf("after 30 days: %v", err)
	}
	s.now = func() time.Time { return now }

	// Rotation: links signed with the old secret keep working.
	rotated, _ := NewSigner(secretB, secretA)
	rotated.now = s.now
	if _, err := rotated.Verify(tok); err != nil {
		t.Fatalf("old secret after rotation: %v", err)
	}
	other, _ := NewSigner(secretB)
	other.now = s.now
	if _, err := other.Verify(tok); !errors.Is(err, ErrSignature) {
		t.Fatalf("unknown secret: %v", err)
	}

	// An unsubscribe token from the same secret is not a confirmation token.
	u, _ := unsubscribe.NewSigner(secretA)
	if _, err := s.Verify(u.Token(unsubscribe.Claims{BrandID: c.BrandID, ContactID: c.ContactID})); err == nil {
		t.Fatal("unsubscribe token accepted as a confirmation")
	}
	if _, err := NewSigner("short"); err == nil {
		t.Fatal("short secret accepted")
	}
}

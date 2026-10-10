package unsubscribe

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

const secretA = "test-secret-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const secretB = "test-secret-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestRoundTrip(t *testing.T) {
	s, err := NewSigner(secretA)
	if err != nil {
		t.Fatal(err)
	}
	brand, contact, camp, topic := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, c := range []Claims{
		{BrandID: brand, ContactID: contact},
		{BrandID: brand, ContactID: contact, CampaignID: camp},
		{BrandID: brand, ContactID: contact, TopicID: topic},
		{BrandID: brand, ContactID: contact, CampaignID: camp, TopicID: topic},
	} {
		tok := s.Token(c)
		if len(tok) > 111 || strings.ContainsAny(tok, "+/=") {
			t.Errorf("token %q is too long or not URL-safe", tok)
		}
		got, err := s.Verify(tok)
		if err != nil || got != c {
			t.Errorf("round trip %+v → %+v, %v", c, got, err)
		}
	}
}

func TestTamperingIsRejected(t *testing.T) {
	s, _ := NewSigner(secretA)
	tok := s.Token(Claims{BrandID: uuid.New(), ContactID: uuid.New(), TopicID: uuid.New()})
	raw, _ := b64.DecodeString(tok)

	// Flip one bit in the contact id: signature must fail.
	raw[10] ^= 1
	if _, err := s.Verify(b64.EncodeToString(raw)); !errors.Is(err, ErrSignature) {
		t.Errorf("tampered contact: %v", err)
	}
	raw[10] ^= 1

	// Drop the topic (shorter payload with the old signature).
	short := append([]byte{}, raw[:3+32]...)
	short[2] = 0
	short = append(short, raw[len(raw)-macLen:]...)
	if _, err := s.Verify(b64.EncodeToString(short)); err == nil {
		t.Error("token with the topic removed was accepted")
	}

	// The decoder would skip newlines; non-canonical spellings are rejected.
	nonCanonical := tok[:10] + "\n" + tok[10:]
	for _, bad := range []string{"", "abc", "!!!!", tok + "x", tok[:len(tok)-2], nonCanonical, tok + "\r\n"} {
		if _, err := s.Verify(bad); err == nil {
			t.Errorf("Verify(%q) succeeded", bad)
		}
	}

	other, _ := NewSigner(secretB)
	if _, err := other.Verify(tok); err == nil {
		t.Error("token verified with another secret")
	}
}

func TestRotation(t *testing.T) {
	oldSigner, _ := NewSigner(secretA)
	c := Claims{BrandID: uuid.New(), ContactID: uuid.New()}
	oldTok := oldSigner.Token(c)

	rotated, err := NewSigner(secretB, secretA)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := rotated.Verify(oldTok); err != nil || got != c {
		t.Fatalf("old link after rotation: %v", err)
	}
	if _, err := oldSigner.Verify(rotated.Token(c)); err == nil {
		t.Fatal("new tokens must use the new secret")
	}
}

func TestSecretLength(t *testing.T) {
	if _, err := NewSigner("short"); err == nil {
		t.Error("short secret accepted")
	}
	if _, err := NewSigner(secretA, "short"); err == nil {
		t.Error("short old secret accepted")
	}
	if _, err := NewSigner(secretA, ""); err != nil {
		t.Errorf("empty old secret should be ignored: %v", err)
	}
}

func TestURLsAndHeaders(t *testing.T) {
	s, _ := NewSigner(secretA)
	c := Claims{BrandID: uuid.New(), ContactID: uuid.New()}
	base := BaseURL("go.brightside.co", "")
	if base != "https://go.brightside.co" || BaseURL("go.brightside.co", "http://localhost:8080/") != "http://localhost:8080" {
		t.Fatalf("BaseURL: %q", base)
	}
	if !strings.HasPrefix(s.PageURL(base, c), "https://go.brightside.co/u/") ||
		!strings.HasPrefix(s.PreferencesURL(base, c), "https://go.brightside.co/p/") {
		t.Fatal("unexpected URLs")
	}
	lu, lup := Headers(s.OneClickURL(base, c))
	if !strings.HasPrefix(lu, "<https://go.brightside.co/u/") || !strings.HasSuffix(lu, ">") || lup != "List-Unsubscribe=One-Click" {
		t.Fatalf("headers: %q %q", lu, lup)
	}
}

// Package unsubscribe makes and checks the signed links in every marketing
// email: the unsubscribe page (P4), the preference center (P3) and the RFC
// 8058 one-click endpoint. A token says who (brand + contact) and optionally
// which campaign and topic the email was about. It's verified with an HMAC,
// no database lookup, and never expires: unsubscribe links must keep working.
//
// The sending worker uses it like this:
//
//	s, _ := unsubscribe.NewSigner(cfg.UnsubscribeSecret, cfg.UnsubscribeSecretOld)
//	base := unsubscribe.BaseURL(brand.LinkDomain, cfg.PublicBaseURLOverride)
//	c := unsubscribe.Claims{BrandID: b, ContactID: c, CampaignID: camp, TopicID: topic}
//	listUnsub, listUnsubPost := unsubscribe.Headers(s.OneClickURL(base, c))
//	footerUnsubscribe := s.PageURL(base, c)
//	footerPreferences := s.PreferencesURL(base, c)
package unsubscribe

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Claims identify the recipient. CampaignID and TopicID are uuid.Nil when unknown.
type Claims struct {
	BrandID    uuid.UUID
	ContactID  uuid.UUID
	CampaignID uuid.UUID // the email that carried the link (for reporting)
	TopicID    uuid.UUID // one-click and the page unsubscribe from this topic; Nil = everything
}

var (
	ErrMalformed = errors.New("unsubscribe: malformed token")
	ErrSignature = errors.New("unsubscribe: bad signature")
)

const (
	version     = 1
	flagCamp    = 1 << 0
	flagTopic   = 1 << 1
	macLen      = 16 // truncated HMAC-SHA256: 128 bits is plenty for a forgery-proof link
	minSecretLn = 32
)

var b64 = base64.RawURLEncoding

type key struct {
	id     byte
	secret []byte
}

// Signer signs with the current secret and also accepts tokens from the old
// one, so the secret can be rotated without breaking links in sent email.
type Signer struct {
	current key
	all     []key
}

// NewSigner needs a secret of at least 32 characters. old may be empty.
func NewSigner(current string, old ...string) (*Signer, error) {
	s := &Signer{}
	for i, sec := range append([]string{current}, old...) {
		if sec == "" && i > 0 {
			continue
		}
		if len(sec) < minSecretLn {
			return nil, fmt.Errorf("unsubscribe: secret must be at least %d characters", minSecretLn)
		}
		h := sha256.Sum256([]byte("unsubscribe-key-id:" + sec))
		k := key{id: h[0], secret: []byte(sec)}
		if i == 0 {
			s.current = k
		}
		s.all = append(s.all, k)
	}
	return s, nil
}

// Token encodes and signs the claims (68–111 URL-safe characters).
func (s *Signer) Token(c Claims) string {
	var flags byte
	payload := []byte{version, s.current.id, 0}
	payload = append(payload, c.BrandID[:]...)
	payload = append(payload, c.ContactID[:]...)
	if c.CampaignID != uuid.Nil {
		flags |= flagCamp
		payload = append(payload, c.CampaignID[:]...)
	}
	if c.TopicID != uuid.Nil {
		flags |= flagTopic
		payload = append(payload, c.TopicID[:]...)
	}
	payload[2] = flags
	return b64.EncodeToString(append(payload, mac(s.current.secret, payload)...))
}

// Verify checks the signature and returns the claims.
func (s *Signer) Verify(token string) (Claims, error) {
	raw, err := b64.DecodeString(token)
	// Only the canonical encoding is accepted (the decoder skips \r and \n),
	// so one link has exactly one spelling.
	if err != nil || len(raw) < 3+32+macLen || raw[0] != version || b64.EncodeToString(raw) != token {
		return Claims{}, ErrMalformed
	}
	payload, sig := raw[:len(raw)-macLen], raw[len(raw)-macLen:]
	flags := payload[2]
	want := 3 + 32
	if flags&flagCamp != 0 {
		want += 16
	}
	if flags&flagTopic != 0 {
		want += 16
	}
	if len(payload) != want || flags&^(flagCamp|flagTopic) != 0 {
		return Claims{}, ErrMalformed
	}
	ok := false
	for _, k := range s.all {
		if k.id == payload[1] && hmac.Equal(sig, mac(k.secret, payload)) {
			ok = true
			break
		}
	}
	if !ok {
		return Claims{}, ErrSignature
	}

	var c Claims
	rest := payload[3:]
	copy(c.BrandID[:], rest[:16])
	copy(c.ContactID[:], rest[16:32])
	rest = rest[32:]
	if flags&flagCamp != 0 {
		copy(c.CampaignID[:], rest[:16])
		rest = rest[16:]
	}
	if flags&flagTopic != 0 {
		copy(c.TopicID[:], rest[:16])
	}
	return c, nil
}

func mac(secret, payload []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("unsubscribe/v1\x00"))
	m.Write(payload)
	return m.Sum(nil)[:macLen]
}

// --- URLs ---

// Public paths, served by the API on each brand's link domain.
const (
	PagePath        = "/u/"    // GET: P4 page · POST: confirm, or RFC 8058 one-click
	PreferencesPath = "/p/"    // GET/POST: P3 preference center
	OneClickPath    = PagePath // RFC 8058: the List-Unsubscribe URL receives the one-click POST
)

// BaseURL is https://<brand link domain>, or the override (local development,
// e.g. http://localhost:8080) when set.
func BaseURL(linkDomain, override string) string {
	if override != "" {
		return strings.TrimSuffix(override, "/")
	}
	return "https://" + linkDomain
}

// PageURL: the "Unsubscribe" footer link (P4).
func (s *Signer) PageURL(base string, c Claims) string { return base + PagePath + s.Token(c) }

// PreferencesURL: the "Manage preferences" footer link (P3).
func (s *Signer) PreferencesURL(base string, c Claims) string {
	return base + PreferencesPath + s.Token(c)
}

// OneClickURL: what goes in the List-Unsubscribe header.
func (s *Signer) OneClickURL(base string, c Claims) string { return base + OneClickPath + s.Token(c) }

// Headers returns the values for the List-Unsubscribe and
// List-Unsubscribe-Post headers (RFC 8058, required by Gmail and Yahoo for
// bulk mail). The email must also be DKIM-signed with both headers covered;
// SES Easy DKIM signs all headers.
func Headers(oneClickURL string) (listUnsubscribe, listUnsubscribePost string) {
	return "<" + oneClickURL + ">", "List-Unsubscribe=One-Click"
}

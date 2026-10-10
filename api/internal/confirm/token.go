// Package confirm is double opt-in: the confirmation email (P2) with a signed
// link, and the page it opens (P1). Re-permission emails to quarantined
// imports use the same link with another purpose.
//
// Tokens work like unsubscribe tokens (HMAC, no database lookup, the same
// secrets with a different label so one can't be passed off as the other),
// but they expire: an old confirmation link shouldn't subscribe anyone.
package confirm

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Purpose of a link.
type Purpose byte

const (
	PurposeOptIn        Purpose = 1 // someone signed up (API, form): double opt-in
	PurposeRepermission Purpose = 2 // an imported, quarantined contact is asked to confirm
	PurposeTopics       Purpose = 3 // a subscriber confirms the topics of one request (RequestID)
)

// TTL: how long a link works.
const TTL = 30 * 24 * time.Hour

type Claims struct {
	BrandID   uuid.UUID
	ContactID uuid.UUID
	Purpose   Purpose
	IssuedAt  time.Time // whole seconds
	// RequestID: the topic request a PurposeTopics link confirms (only that one).
	RequestID uuid.UUID
}

var (
	ErrMalformed = errors.New("confirm: malformed token")
	ErrSignature = errors.New("confirm: bad signature")
	ErrExpired   = errors.New("confirm: link expired")
)

const (
	version     = 1
	macLen      = 16
	minSecretLn = 32
	payloadLen  = 2 + 1 + 16 + 16 + 4 // version, key id, purpose, brand, contact, issued (unix seconds)
	requestLen  = 16                  // PurposeTopics: + topic request id
)

var b64 = base64.RawURLEncoding

type key struct {
	id     byte
	secret []byte
}

// Signer signs with the current secret and accepts the old ones too, so the
// secret can rotate without breaking links in recent email.
type Signer struct {
	current key
	all     []key
	now     func() time.Time
}

func NewSigner(current string, old ...string) (*Signer, error) {
	s := &Signer{now: time.Now}
	for i, sec := range append([]string{current}, old...) {
		if sec == "" && i > 0 {
			continue
		}
		if len(sec) < minSecretLn {
			return nil, fmt.Errorf("confirm: secret must be at least %d characters", minSecretLn)
		}
		h := sha256.Sum256([]byte("confirm-key-id:" + sec))
		k := key{id: h[0], secret: []byte(sec)}
		if i == 0 {
			s.current = k
		}
		s.all = append(s.all, k)
	}
	return s, nil
}

func (s *Signer) Token(c Claims) string {
	p := make([]byte, 0, payloadLen+macLen)
	p = append(p, version, s.current.id, byte(c.Purpose))
	p = append(p, c.BrandID[:]...)
	p = append(p, c.ContactID[:]...)
	p = binary.BigEndian.AppendUint32(p, uint32(c.IssuedAt.Unix()))
	if c.Purpose == PurposeTopics {
		p = append(p, c.RequestID[:]...)
	}
	return b64.EncodeToString(append(p, mac(s.current.secret, p)...))
}

// Verify checks signature, shape and age.
func (s *Signer) Verify(token string) (Claims, error) {
	raw, err := b64.DecodeString(token)
	if err != nil || len(raw) < payloadLen+macLen || raw[0] != version || b64.EncodeToString(raw) != token {
		return Claims{}, ErrMalformed
	}
	want := payloadLen
	if Purpose(raw[2]) == PurposeTopics {
		want += requestLen
	}
	if len(raw) != want+macLen {
		return Claims{}, ErrMalformed
	}
	p, sig := raw[:want], raw[want:]
	ok := false
	for _, k := range s.all {
		if k.id == p[1] && hmac.Equal(sig, mac(k.secret, p)) {
			ok = true
			break
		}
	}
	if !ok {
		return Claims{}, ErrSignature
	}
	c := Claims{Purpose: Purpose(p[2])}
	switch c.Purpose {
	case PurposeOptIn, PurposeRepermission, PurposeTopics:
	default:
		return Claims{}, ErrMalformed
	}
	copy(c.BrandID[:], p[3:19])
	copy(c.ContactID[:], p[19:35])
	c.IssuedAt = time.Unix(int64(binary.BigEndian.Uint32(p[35:39])), 0)
	if c.Purpose == PurposeTopics {
		copy(c.RequestID[:], p[39:55])
	}
	if age := s.now().Sub(c.IssuedAt); age > TTL || age < -time.Hour {
		return c, ErrExpired
	}
	return c, nil
}

func mac(secret, payload []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("confirm/v1\x00"))
	m.Write(payload)
	return m.Sum(nil)[:macLen]
}

// Path of the public confirm page, served on each brand's link domain.
const Path = "/confirm/"

// URL builds the link; base is unsubscribe.BaseURL(brand.LinkDomain, override).
func (s *Signer) URL(base string, c Claims) string { return base + Path + s.Token(c) }

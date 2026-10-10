// Package tracking is click tracking (the main engagement signal) and the
// open pixel (an estimate only), PLAN §6.5.
//
// Every link in a marketing email points at https://<brand link domain>/c/<token>.
// The token carries the real destination and is HMAC-signed, so the redirect
// can't be pointed anywhere else (no open redirect) and needs no database
// lookup: the person is sent on at once and the click is written afterwards.
// Clicks are classified (security scanners, link previews, too fast after
// delivery, every link at once) and bots are marked, never deleted.
//
// Contacts without tracking consent (EU) get links with the NoTrack flag:
// they still work, nothing is recorded.
package tracking

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

// Flags on a token.
const (
	NoTrack  byte = 1 << 0 // no tracking consent: redirect only
	Honeypot byte = 1 << 1 // a hidden link no human sees: whoever clicks is a bot
)

// Link is what a click token says.
type Link struct {
	BrandID   uuid.UUID
	ContactID uuid.UUID
	SendID    int64
	LinkNo    int // 1-based position in the email
	LinkCount int // links in the email (to spot "clicked everything")
	URL       string
	Flags     byte
}

// Pixel is what an open token says.
type Pixel struct {
	BrandID   uuid.UUID
	ContactID uuid.UUID
	SendID    int64
	Flags     byte
}

var (
	ErrMalformed = errors.New("tracking: malformed token")
	ErrSignature = errors.New("tracking: bad signature")
	ErrBadURL    = errors.New("tracking: only http and https links can be tracked")
)

const (
	version     = 1
	macLen      = 16
	minSecretLn = 32
	maxURL      = 4096
	clickFixed  = 3 + 16 + 16 + 8 + 2 + 2 // version, key, flags, brand, contact, send, link no, link count
	openFixed   = 3 + 16 + 16 + 8
)

var b64 = base64.RawURLEncoding

type key struct {
	id     byte
	secret []byte
}

// Signer signs click and open tokens. Same secrets as unsubscribe links,
// different labels, so no token works as another kind.
type Signer struct {
	current key
	all     []key
}

func NewSigner(current string, old ...string) (*Signer, error) {
	s := &Signer{}
	for i, sec := range append([]string{current}, old...) {
		if sec == "" && i > 0 {
			continue
		}
		if len(sec) < minSecretLn {
			return nil, fmt.Errorf("tracking: secret must be at least %d characters", minSecretLn)
		}
		h := sha256.Sum256([]byte("tracking-key-id:" + sec))
		k := key{id: h[0], secret: []byte(sec)}
		if i == 0 {
			s.current = k
		}
		s.all = append(s.all, k)
	}
	return s, nil
}

func mac(secret []byte, label string, p []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(label))
	m.Write(p)
	return m.Sum(nil)[:macLen]
}

// SafeURL: tracked links must be absolute http(s) URLs.
func SafeURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(raw) > maxURL ||
		strings.ContainsAny(raw, "\r\n") {
		return ErrBadURL
	}
	return nil
}

// ClickToken signs a link. The URL must pass SafeURL.
func (s *Signer) ClickToken(l Link) (string, error) {
	if err := SafeURL(l.URL); err != nil {
		return "", err
	}
	if l.LinkNo < 1 || l.LinkNo > 65535 || l.LinkCount < l.LinkNo || l.LinkCount > 65535 {
		return "", fmt.Errorf("tracking: link %d of %d", l.LinkNo, l.LinkCount)
	}
	p := make([]byte, 0, clickFixed+len(l.URL)+macLen)
	p = append(p, version, s.current.id, l.Flags)
	p = append(p, l.BrandID[:]...)
	p = append(p, l.ContactID[:]...)
	p = binary.BigEndian.AppendUint64(p, uint64(l.SendID))
	p = binary.BigEndian.AppendUint16(p, uint16(l.LinkNo))
	p = binary.BigEndian.AppendUint16(p, uint16(l.LinkCount))
	p = append(p, l.URL...)
	return b64.EncodeToString(append(p, mac(s.current.secret, "click/v1\x00", p)...)), nil
}

func (s *Signer) verify(token, label string, minLen int) ([]byte, error) {
	raw, err := b64.DecodeString(token)
	if err != nil || len(raw) < minLen+macLen || raw[0] != version || b64.EncodeToString(raw) != token {
		return nil, ErrMalformed
	}
	p, sig := raw[:len(raw)-macLen], raw[len(raw)-macLen:]
	for _, k := range s.all {
		if k.id == p[1] && hmac.Equal(sig, mac(k.secret, label, p)) {
			return p, nil
		}
	}
	return nil, ErrSignature
}

// Click verifies a click token.
func (s *Signer) Click(token string) (Link, error) {
	p, err := s.verify(token, "click/v1\x00", clickFixed+1)
	if err != nil {
		return Link{}, err
	}
	l := Link{Flags: p[2]}
	copy(l.BrandID[:], p[3:19])
	copy(l.ContactID[:], p[19:35])
	l.SendID = int64(binary.BigEndian.Uint64(p[35:43]))
	l.LinkNo = int(binary.BigEndian.Uint16(p[43:45]))
	l.LinkCount = int(binary.BigEndian.Uint16(p[45:47]))
	l.URL = string(p[47:])
	if SafeURL(l.URL) != nil {
		return Link{}, ErrMalformed
	}
	return l, nil
}

// OpenToken signs the pixel.
func (s *Signer) OpenToken(px Pixel) string {
	p := make([]byte, 0, openFixed+macLen)
	p = append(p, version, s.current.id, px.Flags)
	p = append(p, px.BrandID[:]...)
	p = append(p, px.ContactID[:]...)
	p = binary.BigEndian.AppendUint64(p, uint64(px.SendID))
	return b64.EncodeToString(append(p, mac(s.current.secret, "open/v1\x00", p)...))
}

func (s *Signer) Open(token string) (Pixel, error) {
	p, err := s.verify(token, "open/v1\x00", openFixed)
	if err != nil {
		return Pixel{}, err
	}
	if len(p) != openFixed {
		return Pixel{}, ErrMalformed
	}
	px := Pixel{Flags: p[2]}
	copy(px.BrandID[:], p[3:19])
	copy(px.ContactID[:], p[19:35])
	px.SendID = int64(binary.BigEndian.Uint64(p[35:43]))
	return px, nil
}

// Public paths on each brand's link domain.
const (
	ClickPath = "/c/"
	OpenPath  = "/o/"
)

// ClickURL and OpenURL build the links; base is unsubscribe.BaseURL(…).
func (s *Signer) ClickURL(base string, l Link) (string, error) {
	t, err := s.ClickToken(l)
	return base + ClickPath + t, err
}

func (s *Signer) OpenURL(base string, px Pixel) string {
	return base + OpenPath + s.OpenToken(px) + ".gif"
}

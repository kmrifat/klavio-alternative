// Package webauthntest is a software passkey authenticator for tests. It
// answers navigator.credentials.create/get options the way a browser plus
// platform authenticator would ("none" attestation, ES256 keys).
package webauthntest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
)

var b64 = base64.RawURLEncoding

type credential struct {
	id         []byte
	key        *ecdsa.PrivateKey
	userHandle []byte
	signCount  uint32
}

type Authenticator struct {
	Origin string
	creds  []*credential
}

// options is the part of the server's begin response we need.
type options struct {
	PublicKey struct {
		Challenge string `json:"challenge"`
		RPID      string `json:"rpId"`
		RP        struct {
			ID string `json:"id"`
		} `json:"rp"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		AllowCredentials []struct {
			ID string `json:"id"`
		} `json:"allowCredentials"`
	} `json:"publicKey"`
}

func decode(s string) ([]byte, error) {
	// The server may use padded or unpadded base64url.
	if b, err := b64.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

func (a *Authenticator) clientData(typ, challenge string) []byte {
	cd, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": a.Origin, "crossOrigin": false})
	return cd
}

const (
	flagUP = 0x01 // user present
	flagUV = 0x04 // user verified
	flagBE = 0x08 // backup eligible (synced passkey)
	flagBS = 0x10 // backed up
	flagAT = 0x40 // attested credential data included
)

func authData(rpID string, flags byte, count uint32) []byte {
	h := sha256.Sum256([]byte(rpID))
	out := append([]byte{}, h[:]...)
	out = append(out, flags|flagUP|flagUV|flagBE|flagBS)
	return binary.BigEndian.AppendUint32(out, count)
}

// Create answers a registration begin response ({"publicKey": {...}}).
func (a *Authenticator) Create(optionsJSON []byte) ([]byte, error) {
	var o options
	if err := json.Unmarshal(optionsJSON, &o); err != nil {
		return nil, err
	}
	userHandle, err := decode(o.PublicKey.User.ID)
	if err != nil {
		return nil, fmt.Errorf("user id: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	id := make([]byte, 32)
	_, _ = rand.Read(id)
	c := &credential{id: id, key: key, userHandle: userHandle}
	a.creds = append(a.creds, c)

	pub, err := key.PublicKey.ECDH()
	if err != nil {
		return nil, err
	}
	raw := pub.Bytes() // 0x04 || X || Y
	cose, err := webauthncbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: raw[1:33], -3: raw[33:65]})
	if err != nil {
		return nil, err
	}
	ad := authData(o.PublicKey.RP.ID, flagAT, 0)
	ad = append(ad, make([]byte, 16)...) // AAGUID
	ad = binary.BigEndian.AppendUint16(ad, uint16(len(id)))
	ad = append(ad, id...)
	ad = append(ad, cose...)

	att, err := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": ad})
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"id": b64.EncodeToString(id), "rawId": b64.EncodeToString(id), "type": "public-key",
		"response": map[string]string{
			"clientDataJSON":    b64.EncodeToString(a.clientData("webauthn.create", o.PublicKey.Challenge)),
			"attestationObject": b64.EncodeToString(att),
		},
	})
}

// Get answers a login begin response. With an empty allow list (discoverable
// login) it uses the most recently created credential.
func (a *Authenticator) Get(optionsJSON []byte) ([]byte, error) {
	var o options
	if err := json.Unmarshal(optionsJSON, &o); err != nil {
		return nil, err
	}
	c, err := a.pick(o)
	if err != nil {
		return nil, err
	}
	c.signCount++
	cd := a.clientData("webauthn.get", o.PublicKey.Challenge)
	ad := authData(o.PublicKey.RPID, 0, c.signCount)
	cdHash := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), cdHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, c.key, digest[:])
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"id": b64.EncodeToString(c.id), "rawId": b64.EncodeToString(c.id), "type": "public-key",
		"response": map[string]string{
			"clientDataJSON":    b64.EncodeToString(cd),
			"authenticatorData": b64.EncodeToString(ad),
			"signature":         b64.EncodeToString(sig),
			"userHandle":        b64.EncodeToString(c.userHandle),
		},
	})
}

func (a *Authenticator) pick(o options) (*credential, error) {
	if len(a.creds) == 0 {
		return nil, fmt.Errorf("authenticator has no credentials")
	}
	if len(o.PublicKey.AllowCredentials) == 0 {
		return a.creds[len(a.creds)-1], nil
	}
	for _, allowed := range o.PublicKey.AllowCredentials {
		id, err := decode(allowed.ID)
		if err != nil {
			return nil, err
		}
		for _, c := range a.creds {
			if string(c.id) == string(id) {
				return c, nil
			}
		}
	}
	return nil, fmt.Errorf("none of the allowed credentials are on this authenticator")
}

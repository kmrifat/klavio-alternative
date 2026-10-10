package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// fakeAccess serves a JWKS like https://<team>.cloudflareaccess.com/cdn-cgi/access/certs
// and signs tokens like Cloudflare Access does.
type fakeAccess struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	issuer string
}

func newFakeAccess(t *testing.T) *fakeAccess {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeAccess{key: key}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cdn-cgi/access/certs" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"},
		}})
	}))
	t.Cleanup(f.srv.Close)
	f.issuer = f.srv.URL
	return f
}

func (f *fakeAccess) token(t *testing.T, signer *rsa.PrivateKey, issuer, aud, email string, exp time.Time) string {
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: signer},
		(&jose.SignerOptions{}).WithHeader("kid", "k1").WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := jwt.Signed(sig).Claims(jwt.Claims{
		Issuer: issuer, Audience: jwt.Audience{aud}, Expiry: jwt.NewNumericDate(exp), IssuedAt: jwt.NewNumericDate(time.Now()),
	}).Claims(map[string]any{"email": email}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestAccessVerifier(t *testing.T) {
	f := newFakeAccess(t)
	ctx := oidc.ClientContext(context.Background(), f.srv.Client()) // trust the test TLS cert
	v := NewAccessVerifier(ctx, strings.TrimPrefix(f.issuer, "https://"), "aud-1")
	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	future := time.Now().Add(time.Hour)

	cases := []struct {
		name      string
		token     string
		wantEmail string
	}{
		{"valid", f.token(t, f.key, f.issuer, "aud-1", "maria@acme.test", future), "maria@acme.test"},
		{"no header", "", ""},
		{"other application (aud)", f.token(t, f.key, f.issuer, "aud-2", "maria@acme.test", future), ""},
		{"other team (issuer)", f.token(t, f.key, "https://evil.cloudflareaccess.com", "aud-1", "maria@acme.test", future), ""},
		{"expired", f.token(t, f.key, f.issuer, "aud-1", "maria@acme.test", time.Now().Add(-time.Minute)), ""},
		{"forged signature", f.token(t, otherKey, f.issuer, "aud-1", "maria@acme.test", future), ""},
		{"garbage", "not-a-jwt", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/admin/v1/me", nil).WithContext(ctx)
			if c.token != "" {
				r.Header.Set("Cf-Access-Jwt-Assertion", c.token)
			}
			email, err := v.Email(r)
			if c.wantEmail == "" {
				if err == nil {
					t.Fatalf("expected rejection, got email %q", email)
				}
				return
			}
			if err != nil || email != c.wantEmail {
				t.Fatalf("got %q, %v", email, err)
			}
		})
	}
}

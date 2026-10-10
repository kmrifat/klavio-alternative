package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

// AccessVerifier checks the JWT Cloudflare Access adds to every request that
// passed its login, so the API can't be reached by going around Cloudflare.
type AccessVerifier struct {
	verifier *oidc.IDTokenVerifier
}

// NewAccessVerifier: teamDomain like "acme.cloudflareaccess.com", aud is the
// Access application's AUD tag.
func NewAccessVerifier(ctx context.Context, teamDomain, aud string) *AccessVerifier {
	issuer := "https://" + strings.TrimSuffix(teamDomain, "/")
	keys := oidc.NewRemoteKeySet(ctx, issuer+"/cdn-cgi/access/certs")
	return &AccessVerifier{verifier: oidc.NewVerifier(issuer, keys, &oidc.Config{ClientID: aud})}
}

// Email returns the verified email from the request's Access token.
func (a *AccessVerifier) Email(r *http.Request) (string, error) {
	raw := r.Header.Get("Cf-Access-Jwt-Assertion")
	if raw == "" {
		return "", errors.New("missing Cf-Access-Jwt-Assertion header")
	}
	tok, err := a.verifier.Verify(r.Context(), raw)
	if err != nil {
		return "", err
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := tok.Claims(&claims); err != nil {
		return "", err
	}
	if claims.Email == "" {
		return "", errors.New("access token has no email")
	}
	return claims.Email, nil
}

type accessEmailKey struct{}

func accessEmail(ctx context.Context) string {
	s, _ := ctx.Value(accessEmailKey{}).(string)
	return s
}

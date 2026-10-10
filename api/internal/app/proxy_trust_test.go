package app_test

import (
	"context"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/config"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/unsubscribe"
)

// The test client connects from 127.0.0.1; cloudflared is somewhere else, so
// every CF-* header the client sends is forged.
func untrustedClient(c *config.Config) {
	c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.9.9.9/32")}
}

// A forged CF-IPCountry can neither freeze an account nor pass as "same
// country": it is ignored, and the missing country raises a warning.
func TestForgedCountryIsIgnored(t *testing.T) {
	f := newGovWith(t, untrustedClient)
	carol, carolID := f.carol(t, map[string]string{f.brandID: "editor"})
	carol.country = "US"
	carol.must(200, carol.signIn())

	thief := newBrowser(t, f.srv)
	thief.key, thief.country = carol.key, "RU"
	thief.must(200, thief.signIn()) // not a "new country" freeze: there's no country at all

	signIns, err := f.a.Store.RecentSignIns(context.Background(), uuid.MustParse(carolID), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range signIns {
		if s.Country != "" {
			t.Fatalf("forged country %q was recorded", s.Country)
		}
	}
	kinds := map[string]string{}
	for _, a := range f.ownerA.must(200, f.ownerA.do("GET", "/admin/v1/alerts", nil)).body["alerts"].([]any) {
		m := a.(map[string]any)
		kinds[m["kind"].(string)] = m["severity"].(string)
	}
	if _, ok := kinds["new_country"]; ok {
		t.Fatalf("forged country raised new_country: %v", kinds)
	}
	if kinds["unknown_country"] != "warning" {
		t.Fatalf("sign-in without a trusted country wasn't flagged: %v", kinds)
	}
}

// With Cloudflare trust off (development), the same holds.
func TestCountryIgnoredWhenTrustOff(t *testing.T) {
	f := newGovWith(t, func(c *config.Config) { c.TrustCloudflareHeaders = false })
	carol, _ := f.carol(t, map[string]string{f.brandID: "editor"})
	carol.country = "US"
	carol.must(200, carol.signIn())
	carol.country = "RU"
	carol.must(200, carol.signIn())
	for _, a := range f.ownerA.must(200, f.ownerA.do("GET", "/admin/v1/alerts", nil)).body["alerts"].([]any) {
		if k := a.(map[string]any)["kind"]; k == "new_country" {
			t.Fatal("country header trusted with TRUST_CLOUDFLARE_HEADERS off")
		}
	}
}

// A forged CF-Connecting-IP per request can't spread page POSTs over many
// rate-limit buckets: they all count against the real socket address.
func TestForgedClientIPCantDodgeRateLimit(t *testing.T) {
	f := newUnsubFixtureWith(t, untrustedClient)
	const perIPPageLimit = 120
	for i := range perIPPageLimit + 10 {
		// A fresh contact each time keeps the per-contact limit out of it.
		id := f.newContact(t, "p"+uuid.NewString()[:8]+"@example.com", store.ContactSubscribed, f.newsletter)
		tok := f.a.Unsubscribe.Token(unsubscribe.Claims{BrandID: f.brandID, ContactID: id, TopicID: f.newsletter})
		req, _ := http.NewRequest("POST", f.pub.base+"/u/"+tok, strings.NewReader(url.Values{}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("CF-Connecting-IP", netip.AddrFrom4([4]byte{203, 0, 113, byte(i)}).String())
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode == http.StatusTooManyRequests {
			if i < perIPPageLimit {
				t.Fatalf("limited after %d POSTs, want %d", i, perIPPageLimit)
			}
			return
		}
	}
	t.Fatalf("%d page POSTs with forged client IPs were never rate-limited", perIPPageLimit+10)
}

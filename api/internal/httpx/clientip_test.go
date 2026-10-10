package httpx

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func prefixes(s ...string) []netip.Prefix {
	var out []netip.Prefix
	for _, p := range s {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}

// cloudflared on localhost, or as a sidecar at 172.30.0.10 on a Docker network.
var sidecar = ProxyTrust{Cloudflare: true, Proxies: prefixes("127.0.0.1/32", "::1/128", "172.30.0.10/32")}

func TestResolve(t *testing.T) {
	cases := []struct {
		name        string
		trust       ProxyTrust
		remote      string
		cfIP, cfGeo string
		wantIP      string
		wantCountry string
		wantProxied bool
	}{
		{name: "trusted v4 peer", trust: sidecar, remote: "127.0.0.1:51000",
			cfIP: "203.0.113.7", cfGeo: "DE", wantIP: "203.0.113.7", wantCountry: "DE", wantProxied: true},
		{name: "trusted v6 peer", trust: sidecar, remote: "[::1]:51000",
			cfIP: "2001:db8::7", cfGeo: "us", wantIP: "2001:db8::7", wantCountry: "US", wantProxied: true},
		{name: "trusted docker sidecar", trust: sidecar, remote: "172.30.0.10:40000",
			cfIP: "198.51.100.9", cfGeo: "FR", wantIP: "198.51.100.9", wantCountry: "FR", wantProxied: true},
		{name: "IPv4-mapped IPv6 peer matches the IPv4 range", trust: sidecar, remote: "[::ffff:127.0.0.1]:51000",
			cfIP: "203.0.113.7", cfGeo: "DE", wantIP: "203.0.113.7", wantCountry: "DE", wantProxied: true},
		{name: "IPv4-mapped client IP is unwrapped", trust: sidecar, remote: "127.0.0.1:1",
			cfIP: "::ffff:203.0.113.7", wantIP: "203.0.113.7", wantProxied: true},

		// Spoofing: the headers come from someone who isn't the proxy.
		{name: "untrusted v4 peer spoofs both headers", trust: sidecar, remote: "198.51.100.66:4444",
			cfIP: "203.0.113.7", cfGeo: "DE", wantIP: "198.51.100.66", wantCountry: ""},
		{name: "untrusted v6 peer spoofs both headers", trust: sidecar, remote: "[2001:db8:bad::1]:4444",
			cfIP: "203.0.113.7", cfGeo: "DE", wantIP: "2001:db8:bad::1", wantCountry: ""},
		{name: "docker gateway is not the sidecar", trust: sidecar, remote: "172.30.0.1:4444",
			cfIP: "203.0.113.7", cfGeo: "DE", wantIP: "172.30.0.1", wantCountry: ""},
		{name: "trust off ignores even localhost", trust: ProxyTrust{Proxies: sidecar.Proxies}, remote: "127.0.0.1:51000",
			cfIP: "203.0.113.7", cfGeo: "DE", wantIP: "127.0.0.1", wantCountry: ""},
		{name: "no proxies configured trusts nobody", trust: ProxyTrust{Cloudflare: true}, remote: "127.0.0.1:51000",
			cfIP: "203.0.113.7", cfGeo: "DE", wantIP: "127.0.0.1", wantCountry: ""},

		// Trusted peer, odd header values.
		{name: "trusted peer without headers", trust: sidecar, remote: "127.0.0.1:51000",
			wantIP: "127.0.0.1", wantCountry: "", wantProxied: true},
		{name: "garbage client IP falls back to the peer", trust: sidecar, remote: "127.0.0.1:51000",
			cfIP: "not-an-ip, 1.2.3.4", cfGeo: "DE", wantIP: "127.0.0.1", wantCountry: "DE", wantProxied: true},
		{name: "zoned client IP is rejected", trust: sidecar, remote: "127.0.0.1:51000",
			cfIP: "fe80::1%eth0", wantIP: "127.0.0.1", wantProxied: true},
		{name: "XX means unknown", trust: sidecar, remote: "127.0.0.1:1", cfIP: "203.0.113.7", cfGeo: "XX",
			wantIP: "203.0.113.7", wantCountry: "", wantProxied: true},
		{name: "Tor is kept", trust: sidecar, remote: "127.0.0.1:1", cfIP: "203.0.113.7", cfGeo: "T1",
			wantIP: "203.0.113.7", wantCountry: "T1", wantProxied: true},
		{name: "malformed country is unknown", trust: sidecar, remote: "127.0.0.1:1", cfIP: "203.0.113.7", cfGeo: "USA",
			wantIP: "203.0.113.7", wantCountry: "", wantProxied: true},
		{name: "non-letter country is unknown", trust: sidecar, remote: "127.0.0.1:1", cfIP: "203.0.113.7", cfGeo: "1A",
			wantIP: "203.0.113.7", wantCountry: "", wantProxied: true},

		// RemoteAddr shapes.
		{name: "v6 peer with zone", trust: sidecar, remote: "[fe80::1%eth0]:5", wantIP: "fe80::1"},
		{name: "unparseable remote addr is kept as is", trust: sidecar, remote: "@unix", cfIP: "203.0.113.7", cfGeo: "DE",
			wantIP: "@unix"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = c.remote
			if c.cfIP != "" {
				r.Header.Set("CF-Connecting-IP", c.cfIP)
			}
			if c.cfGeo != "" {
				r.Header.Set("CF-IPCountry", c.cfGeo)
			}
			got := c.trust.Resolve(r)
			if got.IP != c.wantIP || got.Country != c.wantCountry || got.Proxied != c.wantProxied {
				t.Fatalf("got %+v, want ip=%q country=%q proxied=%v", got, c.wantIP, c.wantCountry, c.wantProxied)
			}
		})
	}
}

func TestClientIPWithoutMiddlewareTrustsNothing(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:51000"
	r.Header.Set("CF-Connecting-IP", "203.0.113.7")
	r.Header.Set("CF-IPCountry", "DE")
	if ip, cc := ClientIP(r), ClientCountry(r); ip != "127.0.0.1" || cc != "" {
		t.Fatalf("got %q %q, want the socket address and no country", ip, cc)
	}
}

func TestMiddleware(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	var ip, cc string
	h := sidecar.Middleware(log, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, cc = ClientIP(r), ClientCountry(r)
	}))
	send := func(remote string) {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		r.Header.Set("CF-Connecting-IP", "203.0.113.7")
		r.Header.Set("CF-IPCountry", "DE")
		h.ServeHTTP(httptest.NewRecorder(), r)
	}

	send("127.0.0.1:1")
	if ip != "203.0.113.7" || cc != "DE" {
		t.Fatalf("trusted: got %q %q", ip, cc)
	}
	if logs.Len() != 0 {
		t.Fatalf("trusted request logged: %s", logs.String())
	}

	send("198.51.100.66:1")
	send("198.51.100.67:1")
	if ip != "198.51.100.67" || cc != "" {
		t.Fatalf("spoofed: got %q %q", ip, cc)
	}
	if n := strings.Count(logs.String(), "ignoring Cloudflare headers"); n != 1 {
		t.Fatalf("misconfiguration warning logged %d times, want once: %s", n, logs.String())
	}
}

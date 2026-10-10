package httpx

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
)

// ProxyTrust decides when Cloudflare's CF-Connecting-IP and CF-IPCountry
// headers can be believed. Anyone can send them, so they only count when the
// request's socket peer is a trusted proxy (the cloudflared sidecar); otherwise
// the client is the socket address and the country is unknown.
type ProxyTrust struct {
	Cloudflare bool           // TRUST_CLOUDFLARE_HEADERS
	Proxies    []netip.Prefix // TRUSTED_PROXY_CIDRS
}

// Client is who sent a request, as far as we can tell.
type Client struct {
	IP      string
	Country string // ISO 3166 alpha-2 (or Cloudflare's T1 for Tor); "" = unknown
	// Proxied is true when the Cloudflare headers were trusted.
	Proxied bool
}

// Resolve works out the client for r.
func (t ProxyTrust) Resolve(r *http.Request) Client {
	peer, ok := peerAddr(r.RemoteAddr)
	c := Client{IP: r.RemoteAddr}
	if ok {
		c.IP = peer.String()
	}
	if !ok || !t.Cloudflare || !t.trusts(peer) {
		return c
	}
	c.Proxied = true
	if ip, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); err == nil && ip.Zone() == "" {
		c.IP = ip.Unmap().String()
	}
	c.Country = country(r.Header.Get("CF-IPCountry"))
	return c
}

func (t ProxyTrust) trusts(peer netip.Addr) bool {
	for _, p := range t.Proxies {
		if p.Contains(peer) {
			return true
		}
	}
	return false
}

// peerAddr parses a socket address ("1.2.3.4:5", "[::1]:5"), dropping any IPv6
// zone and unwrapping IPv4-mapped IPv6 so 127.0.0.0/8 matches ::ffff:127.0.0.1.
func peerAddr(remote string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.WithZone("").Unmap(), true
}

// country accepts a two-character code; Cloudflare's XX (unknown) is "".
func country(v string) string {
	v = strings.ToUpper(strings.TrimSpace(v))
	if len(v) != 2 || v == "XX" || v[0] < 'A' || v[0] > 'Z' {
		return ""
	}
	if c := v[1]; (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
		return ""
	}
	return v
}

type clientKey struct{}

// Middleware resolves the client once per request for ClientIP and
// ClientCountry. With Cloudflare trust on, Cloudflare headers from an
// untrusted peer usually mean TRUSTED_PROXY_CIDRS is wrong (or someone is
// bypassing the tunnel); that is logged once.
func (t ProxyTrust) Middleware(log *slog.Logger, next http.Handler) http.Handler {
	var warned atomic.Bool
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := t.Resolve(r)
		if t.Cloudflare && !c.Proxied && r.Header.Get("CF-Connecting-IP") != "" && !warned.Swap(true) {
			log.WarnContext(r.Context(), "ignoring Cloudflare headers from a peer outside TRUSTED_PROXY_CIDRS (logged once)", "peer", c.IP)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientKey{}, c)))
	})
}

func client(r *http.Request) Client {
	if c, ok := r.Context().Value(clientKey{}).(Client); ok {
		return c
	}
	return ProxyTrust{}.Resolve(r) // no middleware: trust nothing
}

// ClientIP is the client's IP: Cloudflare's CF-Connecting-IP when the request
// came through a trusted proxy, else the socket address.
func ClientIP(r *http.Request) string { return client(r).IP }

// ClientCountry is Cloudflare's CF-IPCountry when the request came through a
// trusted proxy; "" (unknown) otherwise.
func ClientCountry(r *http.Request) string { return client(r).Country }

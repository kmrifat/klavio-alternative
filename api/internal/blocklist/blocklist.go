// Package blocklist looks a domain up in the domain blocklists mailbox
// providers use (Spamhaus DBL, SURBL, URIBL). A listing on the sending or
// link domain sends mail to spam regardless of anything else.
//
// These are DNS lookups, but the lists refuse queries from big public
// resolvers (8.8.8.8, 1.1.1.1) and answer with a special "refused" code.
// Use your own recursive resolver (BLOCKLIST_RESOLVER), or for Spamhaus a
// free/paid Data Query Service key (SPAMHAUS_DQS_KEY). Refusals are reported
// as "unknown", never as clean.
package blocklist

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
)

type Status string

const (
	Clean   Status = "clean"
	Listed  Status = "listed"
	Unknown Status = "unknown" // refused, rate-limited or DNS trouble
)

type Result struct {
	Zone   string `json:"zone"`
	Domain string `json:"domain"`
	Status Status `json:"status"`
	// Detail is safe to show anyone: never a raw DNS error, which would
	// contain the query name and so the Spamhaus DQS key.
	Detail string `json:"detail"`
	// Cause is for the server log only: the lookup error, key redacted.
	Cause string `json:"-"`
}

// Resolver is the subset of *net.Resolver used.
type Resolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

type zone struct {
	name      string
	query     func(domain string) string
	interpret func(addrs []string) (Status, string)
}

type Checker struct {
	R      Resolver
	DQSKey string // optional Spamhaus DQS key
}

// NewResolver: server "" uses the system resolver; otherwise every lookup goes
// to that host:port.
func NewResolver(server string) Resolver {
	if server == "" {
		return net.DefaultResolver
	}
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		d := net.Dialer{Timeout: 3 * time.Second}
		return d.DialContext(ctx, network, server)
	}}
}

// Queries are fully qualified (trailing dot) so no search domain is appended.
func (c Checker) zones() []zone {
	dbl := func(d string) string { return d + ".dbl.spamhaus.org." }
	if c.DQSKey != "" {
		dbl = func(d string) string { return d + "." + c.DQSKey + ".dbl.dq.spamhaus.net." }
	}
	return []zone{
		{"Spamhaus DBL", dbl, interpretDBL},
		{"SURBL", func(d string) string { return d + ".multi.surbl.org." }, interpretSURBL},
		{"URIBL", func(d string) string { return d + ".multi.uribl.com." }, interpretURIBL},
	}
}

// Check looks the domain up in every list, in parallel.
func (c Checker) Check(ctx context.Context, domain string) []Result {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	zones := c.zones()
	out := make([]Result, len(zones))
	var wg sync.WaitGroup
	for i, z := range zones {
		wg.Go(func() {
			r := Result{Zone: z.name, Domain: domain}
			qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			addrs, err := c.R.LookupHost(qctx, z.query(domain))
			cancel()
			var de *net.DNSError
			switch {
			case err == nil:
				r.Status, r.Detail = interpret(z, addrs)
			case errors.As(err, &de) && de.IsNotFound:
				r.Status = Clean
			default:
				r.Status, r.Detail = Unknown, "lookup failed (resolver error)"
				if errors.As(err, &de) && de.IsTimeout || errors.Is(err, context.DeadlineExceeded) {
					r.Detail = "lookup failed (timeout)"
				}
				r.Cause = c.redact(err.Error())
			}
			out[i] = r
		})
	}
	wg.Wait()
	return out
}

func (c Checker) redact(s string) string {
	if c.DQSKey == "" {
		return s
	}
	return strings.ReplaceAll(s, c.DQSKey, "<dqs-key>")
}

// interpret rejects answers that aren't blocklist codes at all: a resolver
// that rewrites "no such domain" into an ad server's address (NXDOMAIN
// hijacking) must not make every domain look listed.
func interpret(z zone, addrs []string) (Status, string) {
	for _, a := range addrs {
		ip, err := netip.ParseAddr(a)
		if err != nil || !ip.Is4() || ip.As4()[0] != 127 {
			return Unknown, "resolver problem: unexpected answer " + a + " (the resolver may rewrite missing domains)"
		}
	}
	return z.interpret(addrs)
}

// octets of an IPv4 address (interpret has checked it is 127.x.y.z).
func octets(a string) [4]byte {
	ip, _ := netip.ParseAddr(a)
	return ip.As4()
}

// Spamhaus DBL return codes: 127.0.1.2–255 listed, 127.255.255.x errors
// (https://docs.spamhaus.com/datasets/docs/source/70-access-methods/data-query-service/040-DQS-queries.html).
func interpretDBL(addrs []string) (Status, string) {
	var listed []string
	for _, a := range addrs {
		o := octets(a)
		switch {
		case o[1] == 255 && o[2] == 255:
			if msg, ok := map[byte]string{
				252: "Spamhaus: typing error in the zone name",
				254: "Spamhaus refused the query: it came through a public resolver (set BLOCKLIST_RESOLVER or SPAMHAUS_DQS_KEY)",
				255: "Spamhaus: query limit exceeded",
			}[o[3]]; ok {
				return Unknown, msg
			}
			return Unknown, "Spamhaus: error code " + a
		case o[1] == 0 && o[2] == 1 && o[3] >= 2:
			listed = append(listed, dblReason(o[3]))
		default:
			return Unknown, "Spamhaus: unexpected answer " + a
		}
	}
	if len(listed) == 0 {
		return Clean, ""
	}
	slices.Sort(listed)
	return Listed, strings.Join(slices.Compact(listed), ", ")
}

func dblReason(x byte) string {
	if r, ok := map[byte]string{
		2: "spam domain", 4: "phishing domain", 5: "malware domain", 6: "botnet C&C domain",
		102: "abused legit spam", 103: "abused spammed redirector", 104: "abused legit phish",
		105: "abused legit malware", 106: "abused legit botnet C&C",
	}[x]; ok {
		return r
	}
	return fmt.Sprintf("listed (127.0.1.%d)", x)
}

// bitNames turns a 127.0.0.x bitmask into sorted list names; ok is false when
// x has none of the known bits.
func bitNames(x byte, names map[byte]string) ([]string, bool) {
	var out []string
	for bit, name := range names {
		if x&bit != 0 {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out, len(out) > 0
}

// SURBL answers 127.0.0.x, x a bitmask of lists; 127.0.0.1 = refused.
func interpretSURBL(addrs []string) (Status, string) {
	var lists []string
	for _, a := range addrs {
		o := octets(a)
		if o[1] != 0 || o[2] != 0 {
			return Unknown, "SURBL: unexpected answer " + a
		}
		if o[3] == 1 {
			return Unknown, "SURBL refused the query (public resolver or over the free limit)"
		}
		names, ok := bitNames(o[3], map[byte]string{8: "phishing", 16: "malware", 64: "abused", 128: "cracked"})
		if !ok {
			return Unknown, "SURBL: unexpected answer " + a
		}
		lists = append(lists, names...)
	}
	if len(lists) == 0 {
		return Clean, ""
	}
	slices.Sort(lists)
	return Listed, "SURBL " + strings.Join(slices.Compact(lists), ", ")
}

// URIBL answers 127.0.0.x, x a bitmask: 2 black, 4 grey, 8 red; 127.0.0.1 = refused.
func interpretURIBL(addrs []string) (Status, string) {
	var lists []string
	for _, a := range addrs {
		o := octets(a)
		if o[1] != 0 || o[2] != 0 {
			return Unknown, "URIBL: unexpected answer " + a
		}
		if o[3] == 1 {
			return Unknown, "URIBL refused the query (public resolver or over the free limit)"
		}
		names, ok := bitNames(o[3], map[byte]string{2: "black", 4: "grey (unsolicited bulk mail)", 8: "red"})
		if !ok {
			return Unknown, "URIBL: unexpected answer " + a
		}
		lists = append(lists, names...)
	}
	if len(lists) == 0 {
		return Clean, ""
	}
	slices.Sort(lists)
	return Listed, "URIBL " + strings.Join(slices.Compact(lists), ", ")
}

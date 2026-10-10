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
	Detail string `json:"detail"`
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

func (c Checker) zones() []zone {
	dbl := func(d string) string { return d + ".dbl.spamhaus.org" }
	if c.DQSKey != "" {
		dbl = func(d string) string { return d + "." + c.DQSKey + ".dbl.dq.spamhaus.net" }
	}
	return []zone{
		{"Spamhaus DBL", dbl, interpretDBL},
		{"SURBL", func(d string) string { return d + ".multi.surbl.org" }, interpretSURBL},
		{"URIBL", func(d string) string { return d + ".multi.uribl.com" }, interpretURIBL},
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
				r.Status, r.Detail = z.interpret(addrs)
			case errors.As(err, &de) && de.IsNotFound:
				r.Status = Clean
			default:
				r.Status, r.Detail = Unknown, "lookup failed: "+err.Error()
			}
			out[i] = r
		})
	}
	wg.Wait()
	return out
}

// Spamhaus DBL return codes (https://docs.spamhaus.com/datasets/docs/source/40-real-world-usage/PublicMirrors/MTAs/020-Postfix.html).
func interpretDBL(addrs []string) (Status, string) {
	for _, a := range addrs {
		switch a {
		case "127.255.255.252":
			return Unknown, "Spamhaus: typing error in the zone name"
		case "127.255.255.254":
			return Unknown, "Spamhaus refused the query: it came through a public resolver (set BLOCKLIST_RESOLVER or SPAMHAUS_DQS_KEY)"
		case "127.255.255.255":
			return Unknown, "Spamhaus: query limit exceeded"
		}
	}
	reasons := map[string]string{
		"127.0.1.2": "spam domain", "127.0.1.4": "phishing domain", "127.0.1.5": "malware domain",
		"127.0.1.6": "botnet C&C domain", "127.0.1.102": "abused legit spam", "127.0.1.103": "abused spammed redirector",
		"127.0.1.104": "abused legit phish", "127.0.1.105": "abused legit malware", "127.0.1.106": "abused legit botnet C&C",
	}
	for _, a := range addrs {
		if r, ok := reasons[a]; ok {
			return Listed, r
		}
	}
	if len(addrs) > 0 {
		return Listed, "listed (" + strings.Join(addrs, ", ") + ")"
	}
	return Clean, ""
}

// SURBL answers 127.0.0.x where x is a bitmask of lists; 127.0.0.1 means
// the query was refused.
func interpretSURBL(addrs []string) (Status, string) {
	for _, a := range addrs {
		if a == "127.0.0.1" {
			return Unknown, "SURBL refused the query (public resolver or over the free limit)"
		}
		var b1, b2, b3, x int
		if _, err := fmt.Sscanf(a, "%d.%d.%d.%d", &b1, &b2, &b3, &x); err == nil && b1 == 127 {
			var lists []string
			for bit, name := range map[int]string{8: "phishing", 16: "malware", 64: "abused", 128: "cracked"} {
				if x&bit != 0 {
					lists = append(lists, name)
				}
			}
			return Listed, "SURBL " + strings.Join(lists, ", ")
		}
	}
	return Clean, ""
}

// URIBL: 127.0.0.2 black, .4 grey, .8 red; 127.0.0.1 = refused.
func interpretURIBL(addrs []string) (Status, string) {
	for _, a := range addrs {
		switch a {
		case "127.0.0.1":
			return Unknown, "URIBL refused the query (public resolver or over the free limit)"
		case "127.0.0.2":
			return Listed, "URIBL black"
		case "127.0.0.8":
			return Listed, "URIBL red"
		case "127.0.0.4":
			return Listed, "URIBL grey (unsolicited bulk mail)"
		}
	}
	if len(addrs) > 0 {
		return Listed, "URIBL (" + strings.Join(addrs, ", ") + ")"
	}
	return Clean, ""
}

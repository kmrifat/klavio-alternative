// Package dnscheck looks up a brand's DNS records and says whether each one
// matches what SES needs. It doesn't touch the database.
package dnscheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

type Status string

const (
	OK      Status = "ok"
	Warning Status = "warning" // works, but should be improved (e.g. DMARC p=none)
	Failing Status = "failing" // sending must stay blocked
)

// Record is what we expect to find in DNS.
type Record struct {
	Purpose string // dkim | mail_from_mx | mail_from_spf | dmarc
	Type    string // CNAME | MX | TXT
	Name    string
	Value   string
}

type Result struct {
	Status Status
	Detail string
}

// Resolver is the subset of *net.Resolver we use, so tests can fake DNS.
type Resolver interface {
	LookupCNAME(ctx context.Context, host string) (string, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// PublicResolver queries a public DNS server directly (e.g. "1.1.1.1:53")
// so results don't depend on the host's cache.
func PublicResolver(server string) *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 3 * time.Second}
			return d.DialContext(ctx, network, server)
		},
	}
}

type Checker struct{ R Resolver }

func (c Checker) Check(ctx context.Context, rec Record) Result {
	switch rec.Type {
	case "CNAME":
		return c.cname(ctx, rec)
	case "MX":
		return c.mx(ctx, rec)
	case "TXT":
		if rec.Purpose == "dmarc" {
			return c.dmarc(ctx, rec)
		}
		return c.spf(ctx, rec)
	}
	return Result{Failing, "unknown record type " + rec.Type}
}

func fqdn(s string) string { return strings.ToLower(strings.TrimSuffix(s, ".")) }

func notFound(err error) bool {
	var de *net.DNSError
	return errors.As(err, &de) && de.IsNotFound
}

func lookupFailed(err error) Result {
	if notFound(err) {
		return Result{Failing, "record not found"}
	}
	return Result{Failing, "DNS lookup failed: " + err.Error()}
}

func (c Checker) cname(ctx context.Context, rec Record) Result {
	got, err := c.R.LookupCNAME(ctx, rec.Name)
	if err != nil {
		return lookupFailed(err)
	}
	if fqdn(got) != fqdn(rec.Value) {
		return Result{Failing, fmt.Sprintf("points to %s, expected %s", fqdn(got), fqdn(rec.Value))}
	}
	return Result{OK, ""}
}

// MX value is stored as "10 feedback-smtp.us-east-1.amazonses.com".
func (c Checker) mx(ctx context.Context, rec Record) Result {
	want := rec.Value
	if _, host, ok := strings.Cut(rec.Value, " "); ok {
		want = host
	}
	mxs, err := c.R.LookupMX(ctx, rec.Name)
	if err != nil {
		return lookupFailed(err)
	}
	for _, mx := range mxs {
		if fqdn(mx.Host) == fqdn(want) {
			return Result{OK, ""}
		}
	}
	return Result{Failing, "no MX record pointing to " + fqdn(want)}
}

func (c Checker) spf(ctx context.Context, rec Record) Result {
	txts, err := c.R.LookupTXT(ctx, rec.Name)
	if err != nil {
		return lookupFailed(err)
	}
	var spf []string
	for _, t := range txts {
		if strings.HasPrefix(strings.ToLower(t), "v=spf1") {
			spf = append(spf, t)
		}
	}
	switch {
	case len(spf) == 0:
		return Result{Failing, "no SPF record"}
	case len(spf) > 1:
		return Result{Failing, "more than one SPF record; receivers will reject both"}
	case !slices.Contains(strings.Fields(strings.ToLower(spf[0])), "include:amazonses.com"):
		return Result{Failing, "SPF record doesn't include amazonses.com"}
	}
	return Result{OK, ""}
}

// dmarc checks _dmarc.<sending domain>, then the organisational domain's
// record, which receivers fall back to.
func (c Checker) dmarc(ctx context.Context, rec Record) Result {
	names := []string{rec.Name}
	domain := strings.TrimPrefix(fqdn(rec.Name), "_dmarc.")
	if org, err := publicsuffix.EffectiveTLDPlusOne(domain); err == nil && org != domain {
		names = append(names, "_dmarc."+org)
	}
	for _, name := range names {
		txts, err := c.R.LookupTXT(ctx, name)
		if err != nil && !notFound(err) {
			return lookupFailed(err)
		}
		for _, t := range txts {
			tags := parseTags(t)
			if !strings.EqualFold(tags["v"], "DMARC1") {
				continue
			}
			where := ""
			if name != rec.Name {
				where = " (inherited from " + strings.TrimPrefix(name, "_dmarc.") + ")"
			}
			switch strings.ToLower(tags["p"]) {
			case "quarantine", "reject":
				return Result{OK, "p=" + strings.ToLower(tags["p"]) + where}
			case "none":
				return Result{Warning, "DMARC is p=none" + where + ". Move to p=quarantine once reports are clean."}
			default:
				return Result{Failing, "DMARC record has no valid p= policy" + where}
			}
		}
	}
	return Result{Failing, "no DMARC record"}
}

func parseTags(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(part, "=")
		if ok {
			out[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	return out
}

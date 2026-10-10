package imports

import (
	"context"
	_ "embed"
	"errors"
	"net"
	"net/mail"
	"slices"
	"strings"
	"sync"
	"time"
)

// Outcome is what happens to one row.
type Outcome string

const (
	Ready        Outcome = "ready"        // will be imported and can be emailed
	Quarantine   Outcome = "quarantine"   // imported, gets only one re-permission email
	Existing     Outcome = "existing"     // already a contact: blanks filled in, consent unchanged
	Unsubscribed Outcome = "unsubscribed" // already a contact who unsubscribed: left alone
	Suppressed   Outcome = "suppressed"   // blocked company-wide: left out
	Role         Outcome = "role"         // info@, sales@ …: excluded
	Disposable   Outcome = "disposable"   // throwaway inbox: rejected
	Invalid      Outcome = "invalid"      // not an email address: rejected
	NoMailbox    Outcome = "no_mailbox"   // domain can't receive mail: rejected
	Duplicate    Outcome = "duplicate"    // same address earlier in the file: skipped
)

// Gate limits (S17 "Imports"). Adjustable per company in M5.
const (
	InvalidLimit        = 0.02
	RoleDisposableLimit = 0.05
	QuarantineAfter     = 365 * 24 * time.Hour
)

// Role addresses (info@, sales@ …) and throwaway-inbox domains, one per line.
//
//go:embed role_locals.txt
var roleLocalsTxt string

//go:embed disposable_domains.txt
var disposableDomainsTxt string

var (
	roleLocals        = wordSet(roleLocalsTxt)
	disposableDomains = wordSet(disposableDomainsTxt)
)

func wordSet(txt string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(txt, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			out[strings.ToLower(line)] = true
		}
	}
	return out
}

// NormalizeEmail returns the lower-cased address, or "" if it isn't one.
func NormalizeEmail(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) > 254 || strings.ContainsAny(s, " <>\"") {
		return ""
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s {
		return ""
	}
	local, domain, ok := strings.Cut(s, "@")
	if !ok || local == "" || !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") ||
		strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return ""
	}
	return s
}

func domainOf(email string) string {
	_, d, _ := strings.Cut(email, "@")
	return d
}

func isRole(email string) bool {
	local, _, _ := strings.Cut(email, "@")
	local, _, _ = strings.Cut(local, "+")
	return roleLocals[local]
}

// isDisposable also matches subdomains (x.mailinator.com).
func isDisposable(email string) bool {
	for d := domainOf(email); strings.Contains(d, "."); {
		if disposableDomains[d] {
			return true
		}
		_, d, _ = strings.Cut(d, ".")
	}
	return false
}

// --- MX lookups ---

// MXResolver is the subset of *net.Resolver used to see if a domain takes mail.
type MXResolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// DomainsWithoutMailbox returns domains that definitely can't receive email:
// they don't exist, publish a null MX (RFC 7505), or have neither MX nor A
// records. Lookup errors (timeouts) count as "maybe fine" so a flaky DNS
// server doesn't reject real people.
func DomainsWithoutMailbox(ctx context.Context, r MXResolver, domains []string) []string {
	var (
		mu   sync.Mutex
		bad  []string
		wg   sync.WaitGroup
		jobs = make(chan string)
	)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for d := range jobs {
				if !hasMailbox(ctx, r, d) {
					mu.Lock()
					bad = append(bad, d)
					mu.Unlock()
				}
			}
		}()
	}
	for _, d := range domains {
		jobs <- d
	}
	close(jobs)
	wg.Wait()
	slices.Sort(bad)
	return bad
}

func hasMailbox(ctx context.Context, r MXResolver, domain string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	mxs, err := r.LookupMX(ctx, domain)
	if err == nil && len(mxs) > 0 {
		if len(mxs) == 1 && (mxs[0].Host == "." || mxs[0].Host == "") {
			return false // null MX: "this domain accepts no mail"
		}
		return true
	}
	if err != nil && !isNotFound(err) {
		return true // unknown; don't punish people for our DNS trouble
	}
	// No MX: mail falls back to the domain's A/AAAA record.
	hosts, err := r.LookupHost(ctx, domain)
	if err != nil {
		return !isNotFound(err)
	}
	return len(hosts) > 0
}

func isNotFound(err error) bool {
	var de *net.DNSError
	return errors.As(err, &de) && de.IsNotFound
}

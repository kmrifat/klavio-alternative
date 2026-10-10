// Package contacts holds the rules for contacts, consent, topics, engagement
// tiers and the company-wide suppression list.
package contacts

import (
	"bufio"
	"crypto/sha256"
	_ "embed"
	"errors"
	"net/mail"
	"strings"
)

var ErrInvalidEmail = errors.New("invalid email address")

// NormalizeEmail trims and lower-cases an address and checks its syntax.
// Only a bare address is accepted ("a@b.co", not "Ann <a@b.co>"). Non-ASCII
// addresses are rejected: SES cannot deliver to them without SMTPUTF8.
func NormalizeEmail(raw string) (string, error) {
	addr := strings.ToLower(strings.TrimSpace(raw))
	if addr == "" || len(addr) > 254 {
		return "", ErrInvalidEmail
	}
	for i := 0; i < len(addr); i++ {
		if addr[i] < 0x21 || addr[i] > 0x7e {
			return "", ErrInvalidEmail
		}
	}
	parsed, err := mail.ParseAddress(addr)
	if err != nil || parsed.Name != "" || parsed.Address != addr {
		return "", ErrInvalidEmail
	}
	local, domain, ok := strings.Cut(addr, "@")
	if !ok || len(local) > 64 || !validDomain(domain) {
		return "", ErrInvalidEmail
	}
	return addr, nil
}

// validDomain wants a dotted host name: no IP literals, no single labels.
func validDomain(d string) bool {
	if len(d) > 253 || !strings.Contains(d, ".") {
		return false
	}
	labels := strings.Split(d, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	tld := labels[len(labels)-1]
	return len(tld) >= 2 && strings.Trim(tld, "0123456789") != ""
}

// SuppressionHash is the key of the company-wide suppression list. Only the
// hash is stored, so a suppression survives a GDPR erasure of the contact.
// addr must already be normalized.
func SuppressionHash(addr string) []byte {
	sum := sha256.Sum256([]byte(addr))
	return sum[:]
}

// Domain returns the part after the @ of a normalized address.
func Domain(addr string) string {
	_, d, _ := strings.Cut(addr, "@")
	return d
}

var roleLocals = map[string]bool{}

//go:embed role_locals.txt
var roleLocalsTxt string

var disposableDomains = map[string]bool{}

//go:embed disposable_domains.txt
var disposableDomainsTxt string

func init() {
	loadSet(roleLocals, roleLocalsTxt)
	loadSet(disposableDomains, disposableDomainsTxt)
}

func loadSet(set map[string]bool, txt string) {
	sc := bufio.NewScanner(strings.NewReader(txt))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			set[line] = true
		}
	}
}

// IsRoleAddress reports whether a normalized address goes to a team or a
// system (info@, support@, noreply@ …) rather than one person. Role addresses
// rarely gave consent themselves and complain more often.
func IsRoleAddress(addr string) bool {
	local, _, _ := strings.Cut(addr, "@")
	local, _, _ = strings.Cut(local, "+")
	return roleLocals[local]
}

// IsDisposable reports whether a normalized address is at a throwaway-inbox
// provider, including subdomains of one.
func IsDisposable(addr string) bool {
	d := Domain(addr)
	for {
		if disposableDomains[d] {
			return true
		}
		_, rest, ok := strings.Cut(d, ".")
		if !ok || !strings.Contains(rest, ".") {
			return false
		}
		d = rest
	}
}

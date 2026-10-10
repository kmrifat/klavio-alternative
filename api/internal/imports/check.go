package imports

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/kmrifat/klavio-alternative/api/internal/contacts"
)

// Result is what the checks decided for one row. Only ResultOK and
// ResultQuarantine rows become contacts.
type Result string

const (
	ResultOK            Result = "ok"
	ResultQuarantine    Result = "quarantine"     // valid, but no sign of life for 12+ months and under 4 stars: re-permission email only
	ResultInvalidSyntax Result = "invalid_syntax" // not an email address
	ResultNoMX          Result = "no_mx"          // the domain cannot receive mail
	ResultDisposable    Result = "disposable"     // throwaway inbox
	ResultRole          Result = "role"           // info@, support@ …
	ResultSuppressed    Result = "suppressed"     // on the company-wide suppression list
	ResultDuplicate     Result = "duplicate"      // same address earlier in the file
)

var Results = []Result{ResultOK, ResultQuarantine, ResultInvalidSyntax, ResultNoMX,
	ResultDisposable, ResultRole, ResultSuppressed, ResultDuplicate}

// Imported reports whether a row with this result becomes a contact.
func (r Result) Imported() bool { return r == ResultOK || r == ResultQuarantine }

// QuarantineAfter is how long without any activity before an imported
// contact is quarantined instead of subscribed.
const QuarantineAfter = 365 * 24 * time.Hour

// MXResolver is the slice of *net.Resolver the checks need.
type MXResolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// SuppressionList answers which of the given hashes are suppressed. The
// returned set is keyed by string(hash).
type SuppressionList interface {
	Suppressed(ctx context.Context, hashes [][]byte) (map[string]bool, error)
}

// Checker runs every check on a batch of rows.
type Checker struct {
	Resolver     MXResolver
	Suppressions SuppressionList
	Now          func() time.Time
	// Concurrency is how many domains are looked up at once (default 16).
	Concurrency int
	// LookupTimeout bounds each domain's lookup (default 5s).
	LookupTimeout time.Duration
}

// Check sets Result (and normalizes Email) on every row, in place, and
// returns the counts. Rows are checked in file order, so the first copy of a
// duplicated address is the one that counts.
func (c *Checker) Check(ctx context.Context, rows []Row) (Counts, error) {
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}

	// 1. Syntax, duplicates, role and disposable: no I/O.
	seen := make(map[string]bool, len(rows))
	domains := map[string]bool{}
	var hashes [][]byte
	for i := range rows {
		r := &rows[i]
		addr, err := contacts.NormalizeEmail(r.Email)
		switch {
		case err != nil:
			r.Result = ResultInvalidSyntax
			continue
		case seen[addr]:
			r.Email, r.Result = addr, ResultDuplicate
			continue
		}
		seen[addr] = true
		r.Email = addr
		switch {
		case contacts.IsDisposable(addr):
			r.Result = ResultDisposable
		case contacts.IsRoleAddress(addr):
			r.Result = ResultRole
		default:
			r.Result = ResultOK // provisional
			domains[contacts.Domain(addr)] = true
			hashes = append(hashes, contacts.SuppressionHash(addr))
		}
	}

	// 2. Suppression list, in one round trip.
	suppressed := map[string]bool{}
	if len(hashes) > 0 && c.Suppressions != nil {
		var err error
		if suppressed, err = c.Suppressions.Suppressed(ctx, hashes); err != nil {
			return nil, fmt.Errorf("check suppressions: %w", err)
		}
	}

	// 3. MX, once per domain.
	canReceive, err := c.lookupDomains(ctx, domains)
	if err != nil {
		return nil, err
	}

	// 4. Final verdict per provisional row.
	counts := Counts{}
	for i := range rows {
		r := &rows[i]
		if r.Result == ResultOK {
			switch {
			case suppressed[string(contacts.SuppressionHash(r.Email))]:
				r.Result = ResultSuppressed
			case !canReceive[contacts.Domain(r.Email)]:
				r.Result = ResultNoMX
			case stale(r, now):
				r.Result = ResultQuarantine
			}
		}
		counts[r.Result]++
	}
	return counts, nil
}

// stale is true when nothing in the row shows activity in the last year.
// A row with no dates at all is stale too: we cannot tell, so it is treated
// as the risky case. A 4–5 star rating counts as recent activity.
func stale(r *Row, now time.Time) bool {
	if r.Engaged() {
		return false
	}
	var last *time.Time
	for _, t := range []*time.Time{r.OptedInAt, r.ConfirmedAt, r.LastActivityAt} {
		if t != nil && (last == nil || t.After(*last)) {
			last = t
		}
	}
	return last == nil || now.Sub(*last) > QuarantineAfter
}

func (c *Checker) lookupDomains(ctx context.Context, domains map[string]bool) (map[string]bool, error) {
	res := c.Resolver
	if res == nil {
		res = net.DefaultResolver
	}
	limit := c.Concurrency
	if limit <= 0 {
		limit = 16
	}
	timeout := c.LookupTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	var mu sync.Mutex
	ok := make(map[string]bool, len(domains))
	var g errgroup.Group
	g.SetLimit(limit)
	for d := range domains {
		g.Go(func() error {
			lctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			v := receivesMail(lctx, res, d)
			mu.Lock()
			ok[d] = v
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return ok, nil
}

// receivesMail follows RFC 5321 §5.1: use the MX records, or the domain's
// own address when it has none. A null MX (RFC 7505) means "no mail".
// Lookups that fail for reasons other than "no such domain" count as able to
// receive: a flaky resolver must not mark good addresses invalid.
func receivesMail(ctx context.Context, res MXResolver, domain string) bool {
	mxs, err := res.LookupMX(ctx, domain)
	if err == nil && len(mxs) > 0 {
		for _, mx := range mxs {
			if mx.Host != "." && mx.Host != "" {
				return true
			}
		}
		return false
	}
	if err != nil && !notFound(err) {
		return true
	}
	addrs, err := res.LookupHost(ctx, domain)
	if err != nil {
		return !notFound(err)
	}
	return len(addrs) > 0
}

func notFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

// Counts is the number of rows per result.
type Counts map[Result]int

func (c Counts) Total() int {
	n := 0
	for _, v := range c {
		n += v
	}
	return n
}

// Gate limits, from PLAN.md §6.7. Lists that miss them look bought, scraped
// or long abandoned, and sending to them is how a brand gets blocked.
const (
	MaxInvalidPercent = 2 // invalid syntax + no MX
	MaxRiskyPercent   = 5 // role + disposable
)

// GateResult is the verdict on the list as a whole.
type GateResult struct {
	Passed         bool     `json:"passed"`
	Checked        int      `json:"checked"` // rows the percentages are taken over
	InvalidPercent float64  `json:"invalid_percent"`
	RiskyPercent   float64  `json:"risky_percent"`
	Reasons        []string `json:"reasons,omitempty"`
}

// Gate decides whether the list may be imported at all. Duplicates within
// the file and already-suppressed addresses do not count against the list.
func (c Counts) Gate() GateResult {
	checked := c.Total() - c[ResultDuplicate] - c[ResultSuppressed]
	invalid := c[ResultInvalidSyntax] + c[ResultNoMX]
	risky := c[ResultRole] + c[ResultDisposable]
	g := GateResult{Checked: checked, Passed: true}
	if checked == 0 {
		g.Passed = false
		g.Reasons = append(g.Reasons, "the file has no addresses that could be imported")
		return g
	}
	g.InvalidPercent = percent(invalid, checked)
	g.RiskyPercent = percent(risky, checked)
	if invalid*100 > checked*MaxInvalidPercent {
		g.Passed = false
		g.Reasons = append(g.Reasons, fmt.Sprintf("%.1f%% of addresses are invalid or cannot receive mail (limit %d%%)", g.InvalidPercent, MaxInvalidPercent))
	}
	if risky*100 > checked*MaxRiskyPercent {
		g.Passed = false
		g.Reasons = append(g.Reasons, fmt.Sprintf("%.1f%% are role or throwaway addresses (limit %d%%)", g.RiskyPercent, MaxRiskyPercent))
	}
	return g
}

func percent(n, of int) float64 {
	return float64(int(float64(n)*1000/float64(of)+0.5)) / 10
}

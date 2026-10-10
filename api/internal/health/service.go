package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/publicsuffix"

	"github.com/kmrifat/klavio-alternative/api/internal/blocklist"
	"github.com/kmrifat/klavio-alternative/api/internal/postmaster"
	"github.com/kmrifat/klavio-alternative/api/internal/ses"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/warmup"
)

// ReputationSource reports a brand's own bounce and complaint rates. The
// feedback loop (M4) provides one; until then there is none and the
// reputation part relies on blocklists and Gmail.
type ReputationSource interface {
	Reputation(ctx context.Context, brandID uuid.UUID) (*ReputationSignals, error)
}

// DNSChecker re-checks a brand's DNS and SES status (brands.Service).
type DNSChecker interface {
	Check(ctx context.Context, b store.Brand) (store.Brand, []store.DNSRecord, error)
}

type Config struct {
	// Blocklists nil = blocklist checks are off.
	Blocklists *blocklist.Checker
	// BlocklistEvery: how often the job re-checks (cached in between).
	BlocklistEvery time.Duration
	Postmaster     *postmaster.Client // nil = not connected
	// AccessOn: Cloudflare Access protects the admin app (a protection on S1).
	AccessOn bool
}

type Service struct {
	store  *store.Store
	ses    ses.Provisioner
	dns    DNSChecker
	warm   *warmup.Service
	cfg    Config
	rep    ReputationSource
	log    *slog.Logger
	now    func() time.Time
	acctMu sync.Mutex
	acct   *cachedAccount
}

type cachedAccount struct {
	at  time.Time
	acc ses.Account
	err error
}

func NewService(st *store.Store, prov ses.Provisioner, dns DNSChecker, warm *warmup.Service, cfg Config, log *slog.Logger) *Service {
	if cfg.BlocklistEvery <= 0 {
		cfg.BlocklistEvery = 6 * time.Hour
	}
	return &Service{store: st, ses: prov, dns: dns, warm: warm, cfg: cfg, log: log, now: time.Now}
}

// SetReputationSource plugs in the brand's own bounce/complaint rates (M4).
func (s *Service) SetReputationSource(r ReputationSource) { s.rep = r }

// MinRecheck: a manual recheck re-queries the blocklists at most this often,
// to stay inside the lists' free-use limits.
const MinRecheck = time.Hour

// Compute gathers a brand's inputs and scores it (doesn't save).
func (s *Service) Compute(ctx context.Context, b store.Brand) (Result, error) {
	in := Inputs{Brand: b}
	var err error
	if in.DNS, err = s.store.DNSRecords(ctx, b.ID); err != nil {
		return Result{}, err
	}
	if in.Blocklists, err = s.store.BlocklistResults(ctx, b.ID); err != nil {
		return Result{}, err
	}
	if p, err := s.store.LatestPostmaster(ctx, b.ID); err == nil {
		if s.now().Sub(p.Day) < 14*24*time.Hour { // stale Gmail data says nothing
			in.Postmaster = &p
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return Result{}, err
	}
	if s.rep != nil {
		if in.Signals, err = s.rep.Reputation(ctx, b.ID); err != nil {
			return Result{}, err
		}
	}
	if in.Contacts, err = s.store.ContactStats(ctx, b.ID); err != nil {
		return Result{}, err
	}
	if in.ImportInvalidRate, err = s.store.RecentImportInvalidRate(ctx, b.ID); err != nil {
		return Result{}, err
	}
	if in.Unsubscribes30d, err = s.store.UnsubscribesSince(ctx, b.ID, s.now().Add(-30*24*time.Hour)); err != nil {
		return Result{}, err
	}
	return Compute(in), nil
}

// Recompute scores a brand and saves the result.
func (s *Service) Recompute(ctx context.Context, b store.Brand) (Result, error) {
	r, err := s.Compute(ctx, b)
	if err != nil || r.Score == nil {
		return r, err
	}
	return r, s.store.SaveHealth(ctx, b.ID, *r.Score, r)
}

// RecomputeAll is the hourly job.
func (s *Service) RecomputeAll(ctx context.Context) error {
	return s.eachBrand(ctx, func(b store.Brand) error {
		_, err := s.Recompute(ctx, b)
		return err
	})
}

func (s *Service) eachBrand(ctx context.Context, f func(store.Brand) error) error {
	all, err := s.store.ListBrands(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, b := range all {
		if err := f(b); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", b.Slug, err))
		}
	}
	return errors.Join(errs...)
}

// BlocklistsOn reports whether blocklist checks are configured.
func (s *Service) BlocklistsOn() bool { return s.cfg.Blocklists != nil }

// CheckBlocklists looks up the brand's sending and link domains, unless a
// lookup finished less than minAge ago or another worker is on it. The
// claim is atomic, so parallel re-checks can't multiply queries to the
// lists; a claim that never finishes goes stale after a few minutes.
func (s *Service) CheckBlocklists(ctx context.Context, b store.Brand, minAge time.Duration) error {
	if s.cfg.Blocklists == nil {
		return nil
	}
	if ok, err := s.store.ClaimBlocklistCheck(ctx, b.ID, minAge); err != nil || !ok {
		return err
	}
	// Lists mostly list the registered domain (brand.com), sometimes a
	// subdomain: look up both for the sending and the link domain.
	var domains []string
	for _, d := range []string{b.SendingDomain, b.LinkDomain} {
		if d == "" {
			continue
		}
		org, _ := publicsuffix.EffectiveTLDPlusOne(d)
		for _, x := range []string{d, org} {
			if x != "" && !slices.Contains(domains, x) {
				domains = append(domains, x)
			}
		}
	}
	for _, d := range domains {
		for _, r := range s.cfg.Blocklists.Check(ctx, d) {
			switch {
			case r.Status == blocklist.Listed:
				s.log.WarnContext(ctx, "domain is blocklisted", "brand", b.Slug, "domain", d, "list", r.Zone, "detail", r.Detail)
			case r.Cause != "":
				s.log.WarnContext(ctx, "blocklist lookup failed", "brand", b.Slug, "domain", d, "list", r.Zone, "err", r.Cause)
			}
			err := s.store.SaveBlocklistResult(ctx, store.BlocklistResult{
				BrandID: b.ID, Zone: r.Zone, Domain: r.Domain, Status: string(r.Status), Detail: r.Detail,
			})
			if err != nil {
				return err
			}
		}
	}
	// Only now: a worker that died above leaves the claim to go stale.
	return s.store.CompleteBlocklistCheck(ctx, b.ID)
}

// CheckAllBlocklists is the periodic job: it re-checks every brand whose
// results are older than the interval, then rescores it.
func (s *Service) CheckAllBlocklists(ctx context.Context) error {
	if s.cfg.Blocklists == nil {
		return nil
	}
	return s.eachBrand(ctx, func(b store.Brand) error {
		if err := s.CheckBlocklists(ctx, b, s.cfg.BlocklistEvery-5*time.Minute); err != nil {
			return err
		}
		_, err := s.Recompute(ctx, b)
		return err
	})
}

// PostmasterConnected reports whether Google Postmaster Tools is configured.
func (s *Service) PostmasterConnected() bool { return s.cfg.Postmaster != nil }

// PullPostmaster is the daily job: the last 10 days of Gmail stats for every
// brand's sending domain. Does nothing when not connected.
func (s *Service) PullPostmaster(ctx context.Context) error {
	if s.cfg.Postmaster == nil {
		return nil
	}
	to := s.now().UTC()
	from := to.AddDate(0, 0, -10)
	return s.eachBrand(ctx, func(b store.Brand) error {
		stats, err := s.cfg.Postmaster.TrafficStats(ctx, b.SendingDomain, from, to)
		switch {
		case errors.Is(err, postmaster.ErrDomainNotFound):
			return s.store.SetPostmasterStatus(ctx, b.ID, "domain_not_found",
				"Add "+b.SendingDomain+" in Postmaster Tools and give the service account access")
		case err != nil:
			// The error can carry URLs and token-endpoint replies: log it, show
			// brand viewers something generic.
			s.log.WarnContext(ctx, "postmaster tools", "brand", b.Slug, "err", err)
			return s.store.SetPostmasterStatus(ctx, b.ID, "error", "the request failed; the worker log has details")
		}
		for _, st := range stats {
			err := s.store.SavePostmasterStat(ctx, store.PostmasterStat{
				BrandID: b.ID, Day: st.Day, SpamRatio: st.SpamRatio, DomainReputation: st.DomainReputation,
			})
			if err != nil {
				return err
			}
		}
		if err := s.store.SetPostmasterStatus(ctx, b.ID, "ok", ""); err != nil {
			return err
		}
		_, err = s.Recompute(ctx, b)
		return err
	})
}

// Recheck is S2's "Re-check now": DNS and SES, blocklists (at most hourly),
// then a new score.
func (s *Service) Recheck(ctx context.Context, b store.Brand) (store.Brand, error) {
	b, _, err := s.dns.Check(ctx, b)
	if err != nil {
		return b, err
	}
	if err := s.CheckBlocklists(ctx, b, MinRecheck); err != nil {
		return b, err
	}
	_, err = s.Recompute(ctx, b)
	return b, err
}

// Account returns the shared SES account, cached for a minute.
func (s *Service) Account(ctx context.Context) (ses.Account, error) {
	s.acctMu.Lock()
	defer s.acctMu.Unlock()
	if s.acct != nil && s.now().Sub(s.acct.at) < time.Minute {
		return s.acct.acc, s.acct.err
	}
	acc, err := s.ses.Account(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "SES account", "err", err)
	}
	s.acct = &cachedAccount{at: s.now(), acc: acc, err: err}
	return acc, err
}

// stored returns the saved score, computing and saving it if there's none yet.
func (s *Service) stored(ctx context.Context, b store.Brand, saved map[uuid.UUID]store.BrandHealth) (Result, *time.Time, error) {
	if h, ok := saved[b.ID]; ok {
		var r Result
		if err := json.Unmarshal(h.Components, &r); err == nil {
			return r, &h.ComputedAt, nil
		}
	}
	r, err := s.Recompute(ctx, b)
	if err != nil {
		return r, nil, err
	}
	now := s.now()
	return r, &now, nil
}

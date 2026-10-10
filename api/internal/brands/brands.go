// Package brands creates brands, sets up their SES sending, and checks their
// DNS until they're allowed to send.
package brands

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/kmrifat/klavio-alternative/api/internal/dnscheck"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/ses"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

type Service struct {
	store *store.Store
	ses   ses.Provisioner
	dns   dnscheck.Checker
	log   *slog.Logger
	now   func() time.Time
}

func NewService(st *store.Store, prov ses.Provisioner, dns dnscheck.Checker, log *slog.Logger) *Service {
	return &Service{store: st, ses: prov, dns: dns, log: log, now: time.Now}
}

type CreateInput struct {
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	Color         string `json:"color"`
	PostalAddress string `json:"postal_address"`
	SendingDomain string `json:"sending_domain"`
	LinkDomain    string `json:"link_domain"`
}

var (
	slugRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,30}[a-z0-9]$`)
	colorRe    = regexp.MustCompile(`^#[0-9a-f]{6}$`)
	hostnameRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
)

func bad(code, format string, args ...any) error {
	return httpx.Errorf(http.StatusBadRequest, code, format, args...)
}

// validSubdomain requires a subdomain (news.brand.com, not brand.com), so bulk
// mail never shares a reputation with the company's everyday email.
func validSubdomain(field, d string) error {
	if !hostnameRe.MatchString(d) {
		return bad("invalid_"+field, "%q is not a valid domain name", d)
	}
	org, err := publicsuffix.EffectiveTLDPlusOne(d)
	if err != nil {
		return bad("invalid_"+field, "%q is not under a public domain", d)
	}
	if org == d {
		return bad("invalid_"+field, "use a subdomain such as news.%s, not %s itself — bulk email must stay off the domain your team uses every day", d, d)
	}
	return nil
}

func (in *CreateInput) normalize() error {
	in.Name = strings.TrimSpace(in.Name)
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	in.Color = strings.ToLower(strings.TrimSpace(in.Color))
	in.PostalAddress = strings.TrimSpace(in.PostalAddress)
	in.SendingDomain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(in.SendingDomain), "."))
	in.LinkDomain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(in.LinkDomain), "."))

	switch {
	case in.Name == "":
		return bad("invalid_name", "name is required")
	case !slugRe.MatchString(in.Slug):
		return bad("invalid_slug", "slug must be 3–32 lowercase letters, digits or dashes")
	case !colorRe.MatchString(in.Color):
		return bad("invalid_color", "color must look like #f97316")
	case in.PostalAddress == "":
		return bad("invalid_postal_address", "a postal address is required in every marketing email")
	}
	if err := validSubdomain("sending_domain", in.SendingDomain); err != nil {
		return err
	}
	if err := validSubdomain("link_domain", in.LinkDomain); err != nil {
		return err
	}
	if in.LinkDomain == in.SendingDomain {
		return bad("invalid_link_domain", "use a different subdomain for links, e.g. go.<brand domain>")
	}
	return nil
}

// Create stores the brand, sets it up in SES and records the DNS it needs.
// If SES fails nothing is stored; SES setup is idempotent, so retrying is safe.
func (s *Service) Create(ctx context.Context, r *http.Request, in CreateInput) (store.Brand, error) {
	if err := in.normalize(); err != nil {
		return store.Brand{}, err
	}
	var brand store.Brand
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		var err error
		brand, err = tx.CreateBrand(ctx, store.Brand{
			Slug: in.Slug, Name: in.Name, Color: in.Color, PostalAddress: in.PostalAddress,
			SendingDomain: in.SendingDomain, MailFromDomain: "bounce." + in.SendingDomain,
			LinkDomain: in.LinkDomain, SESConfigurationSet: "brand-" + in.Slug,
		})
		if errors.Is(err, store.ErrConflict) {
			return httpx.Errorf(http.StatusConflict, "brand_exists", "a brand with this slug or one of these domains already exists")
		}
		if err != nil {
			return err
		}

		setup, err := s.ses.SetUpBrand(ctx, brand.SendingDomain, brand.MailFromDomain, brand.SESConfigurationSet)
		if err != nil {
			return fmt.Errorf("setting up SES for %s: %w", brand.SendingDomain, err)
		}
		if err := tx.AddDNSRecords(ctx, brand.ID, RequiredRecords(brand, setup.DKIMTokens, s.ses.Region())); err != nil {
			return err
		}
		if _, err := tx.CreateTopic(ctx, brand.ID, "Newsletter"); err != nil {
			return err
		}
		return tx.Audit(ctx, auditFor(r, "brand.created", brand), nil, brand)
	})
	return brand, err
}

// RequiredRecords is the DNS a brand must publish before it can send.
func RequiredRecords(b store.Brand, dkimTokens []string, region string) []store.DNSRecord {
	var recs []store.DNSRecord
	for _, t := range dkimTokens {
		recs = append(recs, store.DNSRecord{
			Purpose: "dkim", Type: "CNAME",
			Name:  t + "._domainkey." + b.SendingDomain,
			Value: t + ".dkim.amazonses.com",
		})
	}
	org, _ := publicsuffix.EffectiveTLDPlusOne(b.SendingDomain)
	return append(recs,
		store.DNSRecord{Purpose: "mail_from_mx", Type: "MX", Name: b.MailFromDomain, Value: "10 feedback-smtp." + region + ".amazonses.com"},
		store.DNSRecord{Purpose: "mail_from_spf", Type: "TXT", Name: b.MailFromDomain, Value: "v=spf1 include:amazonses.com ~all"},
		// p=none to start; S1/S2 nudge towards quarantine once reports are clean.
		store.DNSRecord{Purpose: "dmarc", Type: "TXT", Name: "_dmarc." + b.SendingDomain, Value: "v=DMARC1; p=none; rua=mailto:dmarc-reports@" + org},
	)
}

// Check re-checks DNS and SES, then moves the brand between setup and ready.
// A paused brand stays paused: only the recovery flow (M4) resumes it.
func (s *Service) Check(ctx context.Context, b store.Brand) (store.Brand, []store.DNSRecord, error) {
	recs, err := s.store.DNSRecords(ctx, b.ID)
	if err != nil {
		return b, nil, err
	}
	now := s.now()
	allPass := true
	for i, rec := range recs {
		res := s.dns.Check(ctx, dnscheck.Record{Purpose: rec.Purpose, Type: rec.Type, Name: rec.Name, Value: rec.Value})
		if res.Status == dnscheck.Failing {
			allPass = false
		}
		if err := s.store.SetDNSRecordStatus(ctx, rec.ID, string(res.Status), res.Detail, now); err != nil {
			return b, nil, err
		}
		recs[i].Status, recs[i].Detail, recs[i].CheckedAt = string(res.Status), res.Detail, &now
	}

	sesStatus, err := s.ses.IdentityStatus(ctx, b.SendingDomain)
	if err != nil {
		s.log.WarnContext(ctx, "SES identity status", "brand", b.Slug, "err", err)
		sesStatus = b.SESIdentityStatus // keep the last known value
	}

	status := store.BrandSetup
	switch {
	case b.Status == store.BrandPaused:
		status = store.BrandPaused
	case allPass && sesStatus == ses.StatusSuccess:
		status = store.BrandReady
	}
	if err := s.store.SetBrandCheckResult(ctx, b.ID, sesStatus, status, now); err != nil {
		return b, nil, err
	}
	if status != b.Status {
		s.log.InfoContext(ctx, "brand status changed", "brand", b.Slug, "from", b.Status, "to", status)
	}
	b.SESIdentityStatus, b.Status, b.DNSCheckedAt = sesStatus, status, &now
	return b, recs, nil
}

// CheckAll runs Check for every brand. Used by the daily job.
func (s *Service) CheckAll(ctx context.Context) error {
	all, err := s.store.ListBrands(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, b := range all {
		if _, _, err := s.Check(ctx, b); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", b.Slug, err))
		}
	}
	return errors.Join(errs...)
}

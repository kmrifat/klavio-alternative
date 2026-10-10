// Package anomaly spots risky account behaviour (S9 "Anomaly alerts"): a
// sign-in from a new country or device, mass exports, import spikes. Risky
// enough → the account is frozen (sessions ended, sign-in blocked) and an
// alert is raised for owners. Owners are never frozen — someone must be able
// to unfreeze — they get a critical alert instead.
package anomaly

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

const (
	lookback            = 90 * 24 * time.Hour
	MassExportRows      = 5000 // one export above this is a mass export
	MassExportPerDay    = 3    // the 3rd export request in 24 hours is too
	ImportSpikeMinRows  = 5000
	ImportSpikeMultiple = 3 // an import 3× the brand's current size
)

type Service struct {
	store *store.Store
	log   *slog.Logger
	// ExpectCountry is set when sign-ins come through Cloudflare, so every
	// one should carry a country: a sign-in without one is flagged even if
	// earlier sign-ins had none either.
	ExpectCountry bool
}

func NewService(st *store.Store, log *slog.Logger) *Service { return &Service{store: st, log: log} }

func (s *Service) freezeOrWarn(ctx context.Context, tx *store.Store, u store.User, kind, msg string, brand *store.Brand, details map[string]any) (bool, error) {
	a := store.Alert{Kind: kind, Severity: "critical", UserID: &u.ID, Message: msg}
	if brand != nil {
		a.BrandID = &brand.ID
	}
	frozen := false
	if u.CompanyRole != store.CompanyOwner {
		if err := tx.Freeze(ctx, u.ID, msg); err != nil {
			return false, err
		}
		frozen = true
		a.Message = msg + " — account frozen until an owner reviews it"
		if err := tx.Audit(ctx, store.AuditEntry{ActorEmail: "system", Action: "user.frozen", TargetType: "user", TargetID: u.Email, BrandID: a.BrandID},
			nil, map[string]any{"reason": msg}); err != nil {
			return false, err
		}
	}
	s.log.WarnContext(ctx, "anomaly", "kind", kind, "user", u.ID, "frozen", frozen)
	return frozen, tx.CreateAlert(ctx, a, details)
}

// CheckSignIn records a passkey sign-in and decides whether it looks like a
// stolen account. country is Cloudflare's CF-IPCountry ("" when unknown or
// not from a trusted proxy — never treated as a known country);
// device identifies the passkey. Returns true if the account is now frozen.
func (s *Service) CheckSignIn(ctx context.Context, tx *store.Store, u store.User, ip, country, device string) (bool, error) {
	prev, err := tx.RecentSignIns(ctx, u.ID, time.Now().Add(-lookback))
	if err != nil {
		return false, err
	}
	if err := tx.RecordSignIn(ctx, u.ID, ip, country, device); err != nil {
		return false, err
	}
	if len(prev) == 0 {
		return false, nil // first sign-in (or first in 90 days): nothing to compare with
	}
	knownCountries := map[string]bool{}
	knownDevices := map[string]bool{}
	for _, p := range prev {
		if p.Country != "" {
			knownCountries[p.Country] = true
		}
		knownDevices[p.Device] = true
	}
	details := map[string]any{"ip": ip, "country": country, "device": device}
	if country != "" && len(knownCountries) > 0 && !knownCountries[country] {
		known := make([]string, 0, len(knownCountries))
		for c := range knownCountries {
			known = append(known, c)
		}
		slices.Sort(known)
		details["known_countries"] = known
		return s.freezeOrWarn(ctx, tx, u, "new_country", fmt.Sprintf("Sign-in for %s from a new country (%s)", u.Email, country), nil, details)
	}
	if country == "" && (len(knownCountries) > 0 || s.ExpectCountry) {
		// Earlier sign-ins came through Cloudflare with a country, or all of
		// them should; this one didn't. Unknown isn't "same": raise it.
		return false, tx.CreateAlert(ctx, store.Alert{
			Kind: "unknown_country", Severity: "warning", UserID: &u.ID,
			Message: fmt.Sprintf("%s signed in without country information (not through Cloudflare?)", u.Email),
		}, details)
	}
	if device != "" && !knownDevices[device] {
		return false, tx.CreateAlert(ctx, store.Alert{
			Kind: "new_device", Severity: "info", UserID: &u.ID,
			Message: fmt.Sprintf("%s signed in with a passkey not used before", u.Email),
		}, details)
	}
	return false, nil
}

// CheckExport flags mass exports. requestsToday includes this one.
func (s *Service) CheckExport(ctx context.Context, tx *store.Store, u store.User, b store.Brand, rows, requestsToday int) (bool, error) {
	if rows <= MassExportRows && requestsToday < MassExportPerDay {
		return false, nil
	}
	msg := fmt.Sprintf("%s requested an export of %d %s contacts (%d export requests in 24 hours)", u.Email, rows, b.Name, requestsToday)
	return s.freezeOrWarn(ctx, tx, u, "mass_export", msg, &b, map[string]any{"rows": rows, "requests_24h": requestsToday})
}

// CheckImport warns when an import is far bigger than the brand's list.
func (s *Service) CheckImport(ctx context.Context, u store.User, b store.Brand, rows, existing int) error {
	if rows <= ImportSpikeMinRows || rows <= existing*ImportSpikeMultiple {
		return nil
	}
	return s.store.CreateAlert(ctx, store.Alert{
		Kind: "import_spike", Severity: "warning", UserID: &u.ID, BrandID: &b.ID,
		Message: fmt.Sprintf("%s uploaded %d contacts for %s, which has %d", u.Email, rows, b.Name, existing),
	}, map[string]any{"rows": rows, "existing": existing})
}

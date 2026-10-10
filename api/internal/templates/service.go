package templates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// Service stores layouts, templates and link allowlists, compiling and
// checking on every save.
type Service struct {
	store *store.Store
	log   *slog.Logger
}

func NewService(st *store.Store, log *slog.Logger) *Service {
	return &Service{store: st, log: log}
}

// layoutFor returns the brand's saved layout, or the default (version 0).
func layoutFor(ctx context.Context, st *store.Store, b store.Brand) (store.Layout, error) {
	l, err := st.LayoutForBrand(ctx, b.ID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Layout{BrandID: b.ID, MJML: DefaultLayout(b.Color)}, nil
	}
	return l, err
}

func checkContext(ctx context.Context, st *store.Store, b store.Brand) (CheckContext, error) {
	list, err := st.LinkAllowlist(ctx, b.ID)
	if err != nil {
		return CheckContext{}, err
	}
	cc := CheckContext{BrandDomains: []string{b.SendingDomain, b.LinkDomain}, PostalAddress: b.PostalAddress}
	for _, d := range list {
		cc.Allowlist = append(cc.Allowlist, d.Domain)
	}
	return cc, nil
}

// build compiles and checks content for storing.
func build(ctx context.Context, layout store.Layout, content string, cc CheckContext) (store.TemplateBuild, []Issue, error) {
	c, issues, err := Build(ctx, layout.MJML, content, cc)
	if err != nil {
		return store.TemplateBuild{}, nil, err
	}
	if issues == nil {
		issues = []Issue{}
	}
	raw, err := json.Marshal(issues)
	if err != nil {
		return store.TemplateBuild{}, nil, err
	}
	b := store.TemplateBuild{HTML: c.HTML, Text: c.Text, Issues: raw, LayoutVersion: layout.Version}
	for _, is := range issues {
		if is.Severity == SeverityError {
			b.ErrorCount++
		} else {
			b.WarningCount++
		}
	}
	return b, issues, nil
}

// rebuildAll recompiles every live template of a brand after its layout or
// allowlist changed. Call it inside the transaction that made the change,
// holding the exclusive brand lock. Returns how many now have errors.
func rebuildAll(ctx context.Context, tx *store.Store, b store.Brand) (rebuilt, withErrors int, err error) {
	layout, err := layoutFor(ctx, tx, b)
	if err != nil {
		return 0, 0, err
	}
	cc, err := checkContext(ctx, tx, b)
	if err != nil {
		return 0, 0, err
	}
	live, err := tx.LiveTemplates(ctx, b.ID)
	if err != nil {
		return 0, 0, err
	}
	for _, t := range live {
		res, _, err := build(ctx, layout, t.MJML, cc)
		if err != nil {
			return 0, 0, fmt.Errorf("rebuild template %s: %w", t.ID, err)
		}
		if err := tx.RebuildTemplate(ctx, t.ID, res); err != nil {
			return 0, 0, err
		}
		if res.ErrorCount > 0 {
			withErrors++
		}
	}
	return len(live), withErrors, nil
}

// auditTemplate is what the audit log keeps about a template: enough to see
// what changed without copying the whole source into the log every save.
func auditTemplate(id uuid.UUID, name, mjml string, b store.TemplateBuild) map[string]any {
	sum := sha256.Sum256([]byte(mjml))
	return map[string]any{
		"id": id, "name": name, "mjml_sha256": hex.EncodeToString(sum[:]), "mjml_bytes": len(mjml),
		"errors": b.ErrorCount, "warnings": b.WarningCount, "layout_version": b.LayoutVersion,
	}
}

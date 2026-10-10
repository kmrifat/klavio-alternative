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

// Rebuilder queues a background recompile of a brand's templates (a river
// job in production; tests run it inline).
type Rebuilder interface {
	QueueRebuild(ctx context.Context, brandID uuid.UUID) error
}

// Service stores layouts, templates and link allowlists, compiling and
// checking on every save.
type Service struct {
	store     *store.Store
	log       *slog.Logger
	rebuilder Rebuilder
}

func NewService(st *store.Store, log *slog.Logger) *Service {
	return &Service{store: st, log: log}
}

// SetRebuilder is called once the job client exists.
func (s *Service) SetRebuilder(r Rebuilder) { s.rebuilder = r }

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

// brandState is everything a template is built against, as of one generation.
type brandState struct {
	generation int64
	layout     store.Layout
	cc         CheckContext
}

// readState reads the generation first, then the layout and allowlist. If
// they change in between, the generation check at write time catches it.
func readState(ctx context.Context, st *store.Store, b store.Brand) (brandState, error) {
	g, err := st.TemplateGeneration(ctx, b.ID)
	if err != nil {
		return brandState{}, err
	}
	l, err := layoutFor(ctx, st, b)
	if err != nil {
		return brandState{}, err
	}
	cc, err := checkContext(ctx, st, b)
	if err != nil {
		return brandState{}, err
	}
	return brandState{generation: g, layout: l, cc: cc}, nil
}

// build compiles and checks content for storing.
func build(ctx context.Context, bs brandState, content string) (store.TemplateBuild, []Issue, error) {
	c, issues, err := Build(ctx, bs.layout.MJML, content, bs.cc)
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
	b := store.TemplateBuild{HTML: c.HTML, Text: c.Text, Issues: raw, LayoutVersion: bs.layout.Version, Generation: bs.generation}
	for _, is := range issues {
		if is.Severity == SeverityError {
			b.ErrorCount++
		} else {
			b.WarningCount++
		}
	}
	return b, issues, nil
}

// queueRebuild starts a background rebuild after a layout or allowlist
// change has committed. If queueing fails the periodic sweep catches up.
func (s *Service) queueRebuild(ctx context.Context, brandID uuid.UUID) {
	if s.rebuilder == nil {
		return
	}
	if err := s.rebuilder.QueueRebuild(ctx, brandID); err != nil {
		s.log.WarnContext(ctx, "queue template rebuild", "brand_id", brandID, "err", err)
	}
}

// RebuildBrand recompiles every stale template of a brand against the
// current generation. It holds no lock while compiling; a template edited
// meanwhile, or a newer layout change, simply wins.
func (s *Service) RebuildBrand(ctx context.Context, brandID uuid.UUID) error {
	b, err := s.store.BrandByID(ctx, brandID)
	if errors.Is(err, store.ErrNotFound) {
		return nil // brand deleted
	}
	if err != nil {
		return err
	}
	bs, err := readState(ctx, s.store, b)
	if err != nil {
		return err
	}
	stale, err := s.store.StaleTemplates(ctx, b.ID, bs.generation)
	if err != nil {
		return err
	}
	var rebuilt, withErrors int
	for _, t := range stale {
		res, _, err := build(ctx, bs, t.MJML)
		if err != nil {
			return fmt.Errorf("rebuild template %s: %w", t.ID, err)
		}
		ok, err := s.store.RebuildTemplate(ctx, b.ID, t.ID, t.Version, res)
		if err != nil {
			return err
		}
		if ok {
			rebuilt++
			if res.ErrorCount > 0 {
				withErrors++
			}
		}
	}
	if len(stale) > 0 {
		s.log.InfoContext(ctx, "rebuilt templates", "brand", b.Slug, "generation", bs.generation, "rebuilt", rebuilt, "with_errors", withErrors)
	}
	return nil
}

// RebuildStale rebuilds every brand that still has stale templates. A
// periodic job runs it in case a queued rebuild was lost.
func (s *Service) RebuildStale(ctx context.Context) error {
	ids, err := s.store.BrandsWithStaleTemplates(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		if err := s.RebuildBrand(ctx, id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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

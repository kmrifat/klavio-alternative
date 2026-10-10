package templates

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/approvals"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// A brand's layout wraps every email it sends, so changing it needs a
// second admin (PLAN.md §5). The change is filed as an approval and applied
// when someone else approves it.

type layoutChange struct {
	BrandID     uuid.UUID `json:"brand_id"`
	FromVersion int       `json:"from_version"`
	MJML        string    `json:"mjml"`
}

// RegisterApprovals registers the layout-change approval with the engine.
func (s *Service) RegisterApprovals(ap *approvals.Service) {
	s.approvals = ap
	ap.Register(approvals.KindLayoutChange, approvals.Definition{
		Label:   "Change a brand's locked layout",
		Execute: s.executeLayoutChange,
	})
}

func (s *Service) executeLayoutChange(ctx context.Context, tx *store.Store, a store.Approval) (any, error) {
	var ch layoutChange
	if err := json.Unmarshal(a.Payload, &ch); err != nil {
		return nil, err
	}
	b, err := tx.BrandByID(ctx, ch.BrandID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, approvals.Reject("the brand no longer exists")
	}
	if err != nil {
		return nil, err
	}
	// The layout may have moved on, or the allowlist changed, since this
	// was requested: check again before applying.
	cc, err := checkContext(ctx, tx, b)
	if err != nil {
		return nil, err
	}
	issues, err := CheckLayout(ctx, ch.MJML, cc)
	if err != nil {
		return nil, err
	}
	if HasErrors(issues) {
		return nil, approvals.Reject("the layout no longer passes every check; ask for the change again")
	}
	saved, err := s.applyLayout(ctx, tx, b, ch, a.RequestedBy, store.AuditEntry{BrandID: &b.ID}, false)
	if errors.Is(err, store.ErrStale) {
		return nil, approvals.Reject("someone changed the layout after this was requested")
	}
	if err != nil {
		return nil, err
	}
	// The rebuild job finds the new generation once this commits; if it
	// runs first, the periodic sweep catches up.
	s.queueRebuild(ctx, b.ID)
	return map[string]any{"version": saved.Version}, nil
}

// applyLayout saves a layout, starts a new template generation and audits
// it, inside tx.
func (s *Service) applyLayout(ctx context.Context, tx *store.Store, b store.Brand, ch layoutChange, by uuid.UUID, audit store.AuditEntry, noSecondAdmin bool) (store.Layout, error) {
	before, err := layoutFor(ctx, tx, b)
	if err != nil {
		return store.Layout{}, err
	}
	saved, err := tx.SaveLayout(ctx, b.ID, ch.MJML, ch.FromVersion, by)
	if err != nil {
		return saved, err
	}
	if _, err := tx.BumpTemplateGeneration(ctx, b.ID); err != nil {
		return saved, err
	}
	audit.Action, audit.TargetType, audit.TargetID, audit.BrandID = "layout.updated", "layout", b.Slug, &b.ID
	return saved, tx.Audit(ctx, audit,
		map[string]any{"version": before.Version, "mjml": before.MJML},
		map[string]any{"version": saved.Version, "mjml": saved.MJML, "no_second_admin": noSecondAdmin})
}

package campaigns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/approvals"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// A send that needs a second person files one approval. Its kind follows
// the most serious reason: size, then a risky audience, then warm-up.
func kindFor(reasons []Reason) approvals.Kind {
	has := func(r Reason) bool {
		for _, x := range reasons {
			if x == r {
				return true
			}
		}
		return false
	}
	switch {
	case has(ReasonLarge) || has(ReasonUnusual):
		return approvals.KindLargeSend
	case has(ReasonAtRisk) || has(ReasonFirstImport):
		return approvals.KindRiskyAudience
	}
	return approvals.KindWarmupSend
}

// approvalPayload ties an approval to the exact campaign version it was
// asked for: any later edit makes it stale.
type approvalPayload struct {
	CampaignID uuid.UUID `json:"campaign_id"`
	Version    int       `json:"version"`
	Audience   int       `json:"audience"`
	Reasons    []Reason  `json:"reasons"`
}

func approvalSummary(c store.Campaign, audience int, reasons []Reason) string {
	why := make([]string, len(reasons))
	for i, r := range reasons {
		why[i] = r.Text()
	}
	return fmt.Sprintf("Send %q to %d people: it %s", c.Name, audience, strings.Join(why, ", and "))
}

// RegisterApprovals tells the approvals engine how to carry out an approved
// send: schedule it, with a fresh cancel window from the moment of approval.
func (s *Service) RegisterApprovals(ap *approvals.Service) {
	s.approvals = ap
	def := approvals.Definition{Execute: s.executeSend, OnReject: s.rejectSend}
	for kind, label := range map[approvals.Kind]string{
		approvals.KindLargeSend:     "Send a large campaign",
		approvals.KindRiskyAudience: "Send to a risky audience",
		approvals.KindWarmupSend:    "Send while the brand warms up",
	} {
		d := def
		d.Label = label
		ap.Register(kind, d)
	}
}

func (s *Service) executeSend(ctx context.Context, tx *store.Store, a store.Approval) (any, error) {
	var p approvalPayload
	if err := json.Unmarshal(a.Payload, &p); err != nil {
		return nil, err
	}
	c, err := tx.LockCampaign(ctx, p.CampaignID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, approvals.Reject("the campaign no longer exists")
	}
	if err != nil {
		return nil, err
	}
	if c.State != store.CampaignNeedsApproval || c.ApprovalID == nil || *c.ApprovalID != a.ID || c.Version != p.Version {
		return nil, approvals.Reject("the campaign changed after this was requested; it has to be confirmed again")
	}
	b, err := tx.BrandByID(ctx, c.BrandID)
	if err != nil {
		return nil, err
	}
	if b.Status != store.BrandReady {
		return nil, approvals.Reject("the brand is %s, so the campaign can't be sent", b.Status)
	}
	startAt := s.now().Add(CancelWindow)
	if c.ScheduledAt != nil && c.ScheduledAt.After(startAt) {
		startAt = *c.ScheduledAt
	}
	if err := tx.ApproveCampaign(ctx, c.ID, startAt); err != nil {
		return nil, err
	}
	// The step runs after the cancel window, long after this commits.
	s.queue(ctx, c.ID, startAt)
	return map[string]any{"campaign_id": c.ID, "start_at": startAt}, nil
}

// rejectSend puts a campaign back to draft when its approval is refused,
// cancelled or expires.
func (s *Service) rejectSend(ctx context.Context, tx *store.Store, a store.Approval) error {
	var p approvalPayload
	if err := json.Unmarshal(a.Payload, &p); err != nil {
		return err
	}
	return tx.ResetCampaignToDraft(ctx, p.CampaignID, a.ID)
}

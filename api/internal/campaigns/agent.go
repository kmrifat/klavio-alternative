package campaigns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/approvals"
	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// An AI agent never schedules a send itself: it asks, and a person approves
// with their passkey. The person whose agent it is may approve it; the
// usual approvals for large or risky sends still follow.

// agentSendPayload ties the request to the campaign version and audience
// the agent saw, like a person's type-to-confirm, and to the template's
// version: the agent can edit templates, so it mustn't swap the content
// after the person looked.
type agentSendPayload struct {
	CampaignID      uuid.UUID  `json:"campaign_id"`
	Version         int        `json:"version"`
	Audience        int        `json:"audience"`
	ScheduledAt     *time.Time `json:"scheduled_at,omitempty"`
	Agent           string     `json:"agent"`
	TemplateVersion int        `json:"template_version"`
}

// errTemplateChanged: the template was edited after the agent asked.
var errTemplateChanged = approvals.Reject("the email's template changed after the agent asked; ask it to request the send again")

// ErrAlreadyRequested: a send request for this campaign is waiting already.
var ErrAlreadyRequested = errors.New("a send request for this campaign is already waiting for approval")

// RequestAgentSend checks the draft as a send would and files the request.
// p is the agent's principal; it needs the sender role on the brand.
func (s *Service) RequestAgentSend(ctx context.Context, b store.Brand, c store.Campaign, scheduledAt *time.Time, p *auth.Principal, audit store.AuditEntry) (store.Approval, Review, error) {
	if c.State != store.CampaignDraft {
		return store.Approval{}, Review{}, ErrNotEditable
	}
	rv, _, err := s.Review(ctx, b, c)
	if err != nil {
		return store.Approval{}, rv, err
	}
	if len(rv.Problems) > 0 {
		return store.Approval{}, rv, &ProblemsError{rv.Problems}
	}
	if s.approvals == nil {
		return store.Approval{}, rv, ErrNeedsApproval
	}
	audience, _ := strconv.Atoi(rv.ConfirmText)
	agent := "an AI agent"
	if p.Agent != nil {
		agent = p.Agent.Label()
	}
	var out store.Approval
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		waiting, err := tx.PendingAgentSend(ctx, c.ID)
		if err != nil {
			return err
		}
		if waiting {
			return ErrAlreadyRequested
		}
		tplVersion, err := tx.TemplateVersionForShare(ctx, b.ID, *c.TemplateID) // Review made sure there is one
		if err != nil {
			return err
		}
		when := "as soon as it's approved"
		if scheduledAt != nil {
			when = "at " + scheduledAt.UTC().Format("2 Jan 15:04 UTC")
		}
		out, err = s.approvals.Request(ctx, tx, approvals.Request{
			Kind: approvals.KindAgentSend, BrandID: &b.ID, RequestedBy: p.User,
			Summary: fmt.Sprintf("Send %q to %d people %s (prepared by %s)", c.Name, audience, when, agent),
			Payload: agentSendPayload{CampaignID: c.ID, Version: c.Version, Audience: audience, ScheduledAt: scheduledAt, Agent: agent, TemplateVersion: tplVersion},
			Audit:   audit,
		})
		return err
	})
	return out, rv, err
}

func (s *Service) registerAgentSend(ap *approvals.Service) {
	ap.Register(approvals.KindAgentSend, approvals.Definition{
		Label: "Send a campaign an AI agent prepared", Execute: s.executeAgentSend, Describe: s.describeAgentSend,
		RequesterMayDecide: true, RequesterRole: store.RoleSender,
	})
}

// executeAgentSend schedules the campaign as the approver: the same checks
// and approvals as if they had pressed Send, held to the audience the agent
// saw.
func (s *Service) executeAgentSend(ctx context.Context, tx *store.Store, a store.Approval) (any, error) {
	var p agentSendPayload
	if err := json.Unmarshal(a.Payload, &p); err != nil {
		return nil, err
	}
	decider := approvals.Decider(ctx)
	if decider == nil {
		return nil, errors.New("campaigns: agent send approved without a decider")
	}
	c, err := tx.LockCampaign(ctx, p.CampaignID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, approvals.Reject("the campaign no longer exists")
	}
	if err != nil {
		return nil, err
	}
	if c.State != store.CampaignDraft || c.Version != p.Version {
		return nil, approvals.Reject("the campaign changed after the agent asked; ask it to request the send again")
	}
	b, err := tx.BrandByID(ctx, c.BrandID)
	if err != nil {
		return nil, err
	}
	if c.TemplateID == nil {
		return nil, errTemplateChanged
	}
	switch v, err := tx.TemplateVersionForShare(ctx, b.ID, *c.TemplateID); {
	case errors.Is(err, store.ErrNotFound):
		return nil, errTemplateChanged
	case err != nil:
		return nil, err
	case v != p.TemplateVersion:
		return nil, errTemplateChanged
	}
	audit := store.AuditEntry{ActorID: &decider.User.ID, ActorEmail: decider.User.Email, BrandID: &b.ID}
	out, rv, err := s.scheduleIn(ctx, tx, b, c, ScheduleInput{Version: p.Version, Confirm: strconv.Itoa(p.Audience), ScheduledAt: p.ScheduledAt}, decider.User, audit)
	var problems *ProblemsError
	switch {
	case errors.As(err, &problems):
		return nil, approvals.Reject("it can't be sent: %s", problems.Error())
	case errors.Is(err, ErrConfirmMismatch):
		return nil, approvals.Reject("the audience is now %s people, not %d; ask the agent to request the send again", rv.ConfirmText, p.Audience)
	case errors.Is(err, ErrNotEditable), errors.Is(err, store.ErrStale):
		return nil, approvals.Reject("the campaign changed after the agent asked; ask it to request the send again")
	case errors.Is(err, ErrNeedsApproval):
		return nil, approvals.Reject("it also needs a second admin's approval, and nobody else can give it")
	case err != nil:
		return nil, err
	}
	res := map[string]any{"campaign_id": out.ID, "state": out.State, "audience": p.Audience}
	if out.StartAt != nil {
		res["start_at"] = out.StartAt
	}
	return res, nil
}

// describeAgentSend: whether the audience still matches what the agent saw.
func (s *Service) describeAgentSend(ctx context.Context, a store.Approval) string {
	var p agentSendPayload
	if json.Unmarshal(a.Payload, &p) != nil {
		return ""
	}
	if a.BrandID == nil {
		return ""
	}
	c, err := s.store.CampaignByID(ctx, *a.BrandID, p.CampaignID)
	if err != nil {
		return "The campaign no longer exists."
	}
	if c.Version != p.Version || c.State != store.CampaignDraft {
		return "The campaign was edited after the agent asked, so this can't be approved; ask the agent again."
	}
	if c.TemplateID == nil {
		return "The campaign has no template any more; ask the agent again."
	}
	if t, err := s.store.TemplateByID(ctx, c.BrandID, *c.TemplateID); err != nil || t.Version != p.TemplateVersion {
		return "The email's template was edited after the agent asked, so this can't be approved; ask the agent again."
	}
	b, err := s.store.BrandByID(ctx, c.BrandID)
	if err != nil {
		return ""
	}
	rv, _, err := s.Review(ctx, b, c)
	if err != nil {
		return ""
	}
	if rv.ConfirmText != strconv.Itoa(p.Audience) {
		return fmt.Sprintf("The audience is now %s people (the agent saw %d), so approving will fail; ask the agent again.", rv.ConfirmText, p.Audience)
	}
	return fmt.Sprintf("Subject: %q. Approving schedules it as if you pressed Send; large or risky sends still need a second admin.", c.Subject)
}

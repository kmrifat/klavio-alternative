package brakes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/approvals"
	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/brands"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/limits"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// Recovery (S8): a paused brand, or the stopped account, only sends again
// after someone works through what went wrong, asks to resume, and a second
// admin agrees (approvals.KindBrandResume). A brand comes back on watch, so
// inactive audiences stay blocked while it proves itself; and only once its
// 7-day rates are back under the pause line, or the next evaluation would
// pause it again at once.

type stepUpper interface {
	RequireStepUp(r *http.Request) (*auth.Principal, error)
}

type Recovery struct {
	Service   *Service
	Store     *store.Store
	Auth      stepUpper
	Approvals *approvals.Service
	Log       *slog.Logger
}

type resumePayload struct {
	BrandID *uuid.UUID `json:"brand_id,omitempty"` // nil = the account brake
	Steps   []string   `json:"steps"`              // what the requester says they did
}

// RegisterApprovals: resuming is a company-level decision (owners and approvers).
func (rc *Recovery) RegisterApprovals() {
	rc.Approvals.Register(approvals.KindBrandResume, approvals.Definition{
		Label: "Resume sending", CompanyLevel: true,
		Execute: func(ctx context.Context, tx *store.Store, a store.Approval) (any, error) {
			var p resumePayload
			if err := json.Unmarshal(a.Payload, &p); err != nil {
				return nil, err
			}
			if p.BrandID == nil {
				return nil, rc.releaseAccount(ctx, tx, a)
			}
			return nil, rc.resumeBrand(ctx, tx, *p.BrandID, a)
		},
		Describe: func(ctx context.Context, a store.Approval) string {
			var p resumePayload
			if json.Unmarshal(a.Payload, &p) != nil || p.BrandID == nil {
				return ""
			}
			still, why, err := rc.stillOver(ctx, rc.Store, *p.BrandID)
			if err != nil || !still {
				return "7-day rates are under the pause line now"
			}
			return "Still over the pause line: " + why
		},
	})
}

// stillOver: would the brand pause again right away?
func (rc *Recovery) stillOver(ctx context.Context, q *store.Store, brandID uuid.UUID) (bool, string, error) {
	f, err := q.FeedbackCounts(ctx, &brandID, rc.Service.now().Add(-Window))
	if err != nil {
		return false, "", err
	}
	var gmail *float64
	if p, err := q.LatestPostmaster(ctx, brandID); err == nil && rc.Service.now().Sub(p.Day) < gmailFresh {
		gmail = p.SpamRatio
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, "", err
	}
	l := BrandLimits{PauseComplaintPct: 0.08}
	if l.PauseBouncePct, err = limits.Read(ctx, q, limits.BrandBouncePct7d); err != nil {
		return false, "", err
	}
	if l.GmailSpamPct, err = limits.Read(ctx, q, limits.BrandGmailSpamPct); err != nil {
		return false, "", err
	}
	state, reasons := Brand(counts(f), gmail, l)
	return state == Paused, strings.Join(reasons, "; "), nil
}

func (rc *Recovery) resumeBrand(ctx context.Context, tx *store.Store, brandID uuid.UUID, a store.Approval) error {
	b, err := tx.BrandByID(ctx, brandID)
	if err != nil {
		return err
	}
	if b.BrakeState != string(Paused) && b.Status != store.BrandPaused {
		return approvals.Reject("%s isn't paused any more", b.Name)
	}
	if still, why, err := rc.stillOver(ctx, tx, brandID); err != nil {
		return err
	} else if still {
		return approvals.Reject("its 7-day rates are still over the pause line (%s); ask again once they've come down", why)
	}
	// Back to ready only if DNS and SES are still right; otherwise to setup.
	recs, err := tx.DNSRecords(ctx, b.ID)
	if err != nil {
		return err
	}
	status := store.BrandReady
	for _, r := range recs {
		if r.Status == "failing" || r.Status == "pending" {
			status = store.BrandSetup
		}
	}
	if b.SESIdentityStatus != "SUCCESS" {
		status = store.BrandSetup
	}
	if err := tx.ResumeBrand(ctx, b.ID, string(Watch), status); err != nil {
		return err
	}
	return tx.AddSafetyEvent(ctx, store.SafetyEvent{Scope: "brand", BrandID: &b.ID, Action: "cleared",
		FromState: string(Paused), ToState: string(Watch), Reason: "resumed after recovery, approved by a second admin"},
		map[string]any{"approval_id": a.ID, "status": status})
}

func (rc *Recovery) releaseAccount(ctx context.Context, tx *store.Store, a store.Approval) error {
	at, _, err := tx.AccountBrake(ctx)
	if err != nil {
		return err
	}
	if at == nil {
		return approvals.Reject("sending isn't stopped any more")
	}
	if err := tx.ReleaseAccountBrake(ctx); err != nil {
		return err
	}
	return tx.AddSafetyEvent(ctx, store.SafetyEvent{Scope: "account", Action: "cleared", FromState: "stopped",
		Reason: "sending resumed after recovery, approved by a second admin"}, map[string]any{"approval_id": a.ID})
}

func (rc *Recovery) Routes(mux *http.ServeMux) {
	f := func(fn httpx.HandlerFunc) http.HandlerFunc { return httpx.Handle(rc.Log, fn) }
	mux.HandleFunc("GET /admin/v1/brands/{slug}/recovery", f(rc.view))
	mux.HandleFunc("POST /admin/v1/brands/{slug}/recovery/resume", f(rc.requestBrand))
	mux.HandleFunc("GET /admin/v1/account-brake", f(rc.accountView))
	mux.HandleFunc("POST /admin/v1/account-brake/release", f(rc.requestAccount))
}

// Step is one item on the recovery checklist (S8).
type Step struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Help  string `json:"help"`
}

// BrandSteps is the S8 checklist.
var BrandSteps = []Step{
	{"cause", "Find what caused it", "Look at the safety event below and the last campaigns: which list, which import, which email?"},
	{"list", "Clean the list", "Remove or quarantine the contacts it came from: old imports, bought or scraped lists, people who never engaged."},
	{"content", "Check the content", "Misleading subject lines, missing unsubscribe, link shorteners and spammy wording all raise complaints."},
	{"auth", "Check authentication", "DKIM, SPF and DMARC must all pass (Brand health)."},
	{"plan", "Plan a careful restart", "Start with your most engaged contacts (Active tier) and small sends; the brand resumes on watch."},
}

type recoveryView struct {
	Brand     store.Brand         `json:"brand"`
	Paused    bool                `json:"paused"`
	Event     *store.SafetyEvent  `json:"event"` // what paused it
	Counts    Counts              `json:"counts"`
	StillOver bool                `json:"still_over"`
	Why       string              `json:"why"`
	Steps     []Step              `json:"steps"`
	Pending   *store.Approval     `json:"pending"`
	Events    []store.SafetyEvent `json:"events"`
}

func (rc *Recovery) view(w http.ResponseWriter, r *http.Request) error {
	b, _, err := brands.Visible(r, rc.Store)
	if err != nil {
		return err
	}
	ctx := r.Context()
	v := recoveryView{Brand: b, Paused: b.BrakeState == string(Paused) || b.Status == store.BrandPaused, Steps: BrandSteps}
	if v.Events, err = rc.Store.SafetyEvents(ctx, b.ID, 20); err != nil {
		return err
	}
	for i := range v.Events {
		if e := v.Events[i]; e.Scope == "brand" && e.Action == "paused" {
			v.Event = &v.Events[i]
			break
		}
	}
	if v.Events == nil {
		v.Events = []store.SafetyEvent{}
	}
	f, err := rc.Store.FeedbackCounts(ctx, &b.ID, rc.Service.now().Add(-Window))
	if err != nil {
		return err
	}
	v.Counts = counts(f)
	if v.StillOver, v.Why, err = rc.stillOver(ctx, rc.Store, b.ID); err != nil {
		return err
	}
	if v.Pending, err = rc.pending(ctx, &b.ID); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, v)
	return nil
}

// pending: an open resume request for the brand (nil brand = the account).
func (rc *Recovery) pending(ctx context.Context, brandID *uuid.UUID) (*store.Approval, error) {
	return rc.pendingTx(ctx, rc.Store, brandID)
}

type resumeInput struct {
	Steps  []string `json:"steps"`
	Reason string   `json:"reason"`
}

func (in resumeInput) check(steps []Step) error {
	done := map[string]bool{}
	for _, s := range in.Steps {
		done[s] = true
	}
	var missing []string
	for _, s := range steps {
		if !done[s.ID] {
			missing = append(missing, s.Title)
		}
	}
	if len(missing) > 0 {
		return httpx.Errorf(http.StatusBadRequest, "checklist_incomplete", "work through every step first: %s", strings.Join(missing, ", "))
	}
	if len(strings.TrimSpace(in.Reason)) < 10 {
		return httpx.Errorf(http.StatusBadRequest, "reason_required", "say what you found and fixed; the approver reads it")
	}
	return nil
}

func (rc *Recovery) requestBrand(w http.ResponseWriter, r *http.Request) error {
	b, _, err := brands.WithRole(r, rc.Store, store.RoleBrandAdmin)
	if err != nil {
		return err
	}
	p, err := rc.Auth.RequireStepUp(r)
	if err != nil {
		return err
	}
	var in resumeInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	if b.BrakeState != string(Paused) && b.Status != store.BrandPaused {
		return httpx.Errorf(http.StatusConflict, "not_paused", "%s isn't paused", b.Name)
	}
	if err := in.check(BrandSteps); err != nil {
		return err
	}
	ctx := r.Context()
	if still, why, err := rc.stillOver(ctx, rc.Store, b.ID); err != nil {
		return err
	} else if still {
		return httpx.Errorf(http.StatusConflict, "still_over_limit",
			"the 7-day rates are still over the pause line (%s). They come down as the window moves on while nothing is sent.", why)
	}
	return rc.request(w, r, p, &b, in, fmt.Sprintf("Resume sending for %s (paused by its brake)", b.Name))
}

func (rc *Recovery) accountView(w http.ResponseWriter, r *http.Request) error {
	p, err := auth.Require(r)
	if err != nil {
		return err
	}
	if !p.SeesAllBrands() {
		return httpx.ErrForbidden
	}
	at, reason, err := rc.Store.AccountBrake(r.Context())
	if err != nil {
		return err
	}
	pend, err := rc.pending(r.Context(), nil)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stopped_at": at, "reason": reason, "pending": pend})
	return nil
}

var accountSteps = []Step{
	{"cause", "Find the brand and campaign behind it", "Check each brand's safety events and complaint rates."},
	{"brands", "Pause or fix the brands involved", "A brand with high complaints should stay paused until its recovery."},
	{"amazon", "Check the AWS console", "If Amazon paused the account, follow its notice; resuming here doesn't lift Amazon's own pause."},
}

func (rc *Recovery) requestAccount(w http.ResponseWriter, r *http.Request) error {
	p, err := rc.Auth.RequireStepUp(r)
	if err != nil {
		return err
	}
	if !p.IsOwner() {
		return httpx.ErrForbidden
	}
	var in resumeInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	at, _, err := rc.Store.AccountBrake(r.Context())
	if err != nil {
		return err
	}
	if at == nil {
		return httpx.Errorf(http.StatusConflict, "not_stopped", "sending isn't stopped")
	}
	if err := in.check(accountSteps); err != nil {
		return err
	}
	return rc.request(w, r, p, nil, in, "Resume sending for every brand (account brake)")
}

func (rc *Recovery) request(w http.ResponseWriter, r *http.Request, p *auth.Principal, b *store.Brand, in resumeInput, summary string) error {
	ctx := r.Context()
	var brandID *uuid.UUID
	if b != nil {
		brandID = &b.ID
	}
	var out store.Approval
	err := rc.Store.InTx(ctx, func(tx *store.Store) error {
		lock := "recovery:account"
		if brandID != nil {
			lock = "recovery:" + brandID.String()
		}
		if err := tx.Lock(ctx, lock); err != nil {
			return err
		}
		if prev, err := rc.pendingTx(ctx, tx, brandID); err != nil {
			return err
		} else if prev != nil {
			return httpx.Errorf(http.StatusConflict, "already_requested", "a resume request is already waiting for approval")
		}
		var err error
		out, err = rc.Approvals.Request(ctx, tx, approvals.Request{
			Kind: approvals.KindBrandResume, BrandID: brandID, RequestedBy: p.User, Summary: summary, Reason: in.Reason,
			Payload: resumePayload{BrandID: brandID, Steps: in.Steps},
			Audit:   auth.AuditEntry(r, "", "", "", brandID),
		})
		if errors.Is(err, approvals.ErrNoApprover) {
			// Never resume alone: with one admin there's nobody to check.
			return httpx.Errorf(http.StatusConflict, "no_approver", "resuming needs a second admin (an owner or approver); invite one first")
		}
		return err
	})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"approval": out})
	return nil
}

func (rc *Recovery) pendingTx(ctx context.Context, tx *store.Store, brandID *uuid.UUID) (*store.Approval, error) {
	list, err := tx.ListApprovals(ctx, store.ApprovalFilter{Status: store.ApprovalPending, Limit: 200})
	if err != nil {
		return nil, err
	}
	for i, a := range list {
		if a.Kind != string(approvals.KindBrandResume) || a.ExpiresAt.Before(rc.Service.now()) {
			continue
		}
		var p resumePayload
		if json.Unmarshal(a.Payload, &p) != nil {
			continue
		}
		if (brandID == nil && p.BrandID == nil) || (brandID != nil && p.BrandID != nil && *brandID == *p.BrandID) {
			return &list[i], nil
		}
	}
	return nil, nil
}

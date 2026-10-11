// Package approvals is the two-person rule (S9 "Needs a second person", S15).
// A feature files a request instead of acting; another eligible admin approves
// it; the feature's Execute function then runs in the same transaction as the
// decision, so "approved" and "done" can't drift apart.
//
// Using it from a feature:
//
//	svc.Register(approvals.KindSafetyLimit, approvals.Definition{
//		Label: "Loosen a safety limit", CompanyLevel: true,
//		Execute: func(ctx context.Context, tx *store.Store, a store.Approval) (any, error) { … },
//	})
//	a, err := svc.Request(ctx, tx, approvals.Request{Kind: …, RequestedBy: user, Summary: "…", Payload: p})
package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

type Kind string

// Every approval type in S9. Kinds without an executor yet are registered by
// the milestone that builds the feature.
const (
	KindLargeSend     Kind = "large_send"     // M3: over 10,000 people or 2× the brand's usual size
	KindRiskyAudience Kind = "risky_audience" // M3: at-risk contacts, first send to an import
	KindWarmupSend    Kind = "warmup_send"    // M3: every send while a brand warms up
	KindExport        Kind = "export"         // exports with personal data
	KindRoleChange    Kind = "role_change"    // granting owner, approver or brand admin
	KindAdminInvite   Kind = "admin_invite"   // inviting someone with admin rights
	KindSafetyLimit   Kind = "safety_limit"   // loosening a safety limit
	KindBrandResume   Kind = "brand_resume"   // M4: resuming a paused brand
	KindAPIKey        Kind = "api_key"        // M7: creating an API key
	KindLayoutChange  Kind = "layout_change"  // M3: changing a brand's locked layout
	KindAgentSend     Kind = "agent_send"     // a send an AI agent prepared (MCP)
)

// TTL: requests nobody decided on expire.
const TTL = 7 * 24 * time.Hour

type Definition struct {
	Label string
	// CompanyLevel: only owners and company approvers may decide. Otherwise
	// the brand's admins can decide too (for requests with a brand).
	CompanyLevel bool
	// RequesterMayDecide: the requester may decide too, if they hold
	// RequesterRole on the request's brand. For work an agent prepared on
	// someone's behalf: the agent and the person are already two parties.
	RequesterMayDecide bool
	RequesterRole      store.BrandRole
	// Execute performs the approved action inside the decision's transaction.
	// Its result is stored on the approval (shown to the requester). An error
	// rolls everything back and the request stays pending.
	Execute func(ctx context.Context, tx *store.Store, a store.Approval) (any, error)
	// OnReject optionally cleans up when a request is rejected, cancelled or expires.
	OnReject func(ctx context.Context, tx *store.Store, a store.Approval) error
	// Describe optionally adds live detail for the approver (e.g. "1,204
	// contacts match now"), computed when the list is shown.
	Describe func(ctx context.Context, a store.Approval) string
}

// RejectError, returned by Execute, closes the request as rejected with this
// reason instead of rolling back: the request can no longer be carried out
// (stale, requester lost their rights). Side effects Execute made before
// returning it (e.g. freezing an account) are kept.
type RejectError struct{ Reason string }

func (e *RejectError) Error() string { return "rejected: " + e.Reason }

func Reject(format string, args ...any) error {
	return &RejectError{Reason: fmt.Sprintf(format, args...)}
}

type Service struct {
	store *store.Store
	log   *slog.Logger
	now   func() time.Time
	mu    sync.RWMutex
	defs  map[Kind]Definition
}

func NewService(st *store.Store, log *slog.Logger) *Service {
	return &Service{store: st, log: log, now: time.Now, defs: map[Kind]Definition{}}
}

func (s *Service) Register(k Kind, d Definition) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defs[k] = d
}

func (s *Service) def(k string) (Definition, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.defs[Kind(k)]
	return d, ok
}

func (s *Service) Label(k string) string {
	if d, ok := s.def(k); ok && d.Label != "" {
		return d.Label
	}
	return k
}

type Request struct {
	Kind        Kind
	BrandID     *uuid.UUID
	RequestedBy store.User
	Summary     string // one line for the approver
	Reason      string // why, from the requester
	Payload     any
	// Who the request is about, if anyone: they can't decide it. Set
	// TargetUserID for an existing user, TargetEmail for an invite.
	TargetUserID *uuid.UUID
	TargetEmail  string
	// Audit context (who/where), usually from auth.AuditEntry(r, …).
	Audit store.AuditEntry
}

var (
	ErrNoApprover = errors.New("approvals: nobody else can approve this")
	ErrUnknown    = errors.New("approvals: unknown kind")
)

// Request files an approval inside the caller's transaction, so the feature's
// own state (e.g. a pending export) and the request are created together.
func (s *Service) Request(ctx context.Context, tx *store.Store, r Request) (store.Approval, error) {
	d, ok := s.def(string(r.Kind))
	if !ok {
		return store.Approval{}, fmt.Errorf("%w: %s", ErrUnknown, r.Kind)
	}
	if !d.RequesterMayDecide {
		ok, err := s.SomeoneElseCanApprove(ctx, tx, r.RequestedBy.ID, r.BrandID, d.CompanyLevel)
		if err != nil {
			return store.Approval{}, err
		}
		if !ok {
			return store.Approval{}, ErrNoApprover
		}
	}
	payload, err := json.Marshal(r.Payload)
	if err != nil {
		return store.Approval{}, err
	}
	var targetEmail *string
	if r.TargetEmail != "" {
		e := strings.ToLower(r.TargetEmail)
		targetEmail = &e
	}
	a, err := tx.CreateApproval(ctx, store.Approval{
		Kind: string(r.Kind), BrandID: r.BrandID, RequestedBy: r.RequestedBy.ID,
		Summary: r.Summary, Reason: r.Reason, Payload: payload, ExpiresAt: s.now().Add(TTL),
		TargetUserID: r.TargetUserID, TargetEmail: targetEmail,
	})
	if err != nil {
		return a, err
	}
	e := r.Audit
	e.Action, e.TargetType, e.TargetID, e.BrandID = "approval.requested", "approval", a.ID.String(), r.BrandID
	return a, tx.Audit(ctx, e, nil, map[string]any{"kind": r.Kind, "summary": r.Summary, "reason": r.Reason})
}

// ShortcutAllowed: may an admin change skip approval because nobody else
// could approve? Only while the company has never had two admins (the
// bootstrap); afterwards demoting or freezing others can't reopen it.
func (s *Service) ShortcutAllowed(ctx context.Context, tx *store.Store) (bool, error) {
	had, err := tx.HadTwoAdmins(ctx)
	return !had, err
}

// SomeoneElseCanApprove: is there an unfrozen admin, other than the requester, who could decide?
func (s *Service) SomeoneElseCanApprove(ctx context.Context, tx *store.Store, requester uuid.UUID, brandID *uuid.UUID, companyLevel bool) (bool, error) {
	n, err := tx.OtherAdmins(ctx, requester)
	if err != nil || n > 0 || companyLevel || brandID == nil {
		return n > 0, err
	}
	n, err = tx.OtherBrandAdmins(ctx, *brandID, requester)
	return n > 0, err
}

// isSubject: is this person who the request is about? (Approving your own
// promotion is self-approval in substance.)
func isSubject(p *auth.Principal, a store.Approval) bool {
	return (a.TargetUserID != nil && *a.TargetUserID == p.User.ID) ||
		(a.TargetEmail != nil && strings.EqualFold(*a.TargetEmail, p.User.Email))
}

// CanDecide: may this person approve or reject the request? Never an agent.
func (s *Service) CanDecide(p *auth.Principal, a store.Approval) bool {
	if p == nil || p.Agent != nil || p.User.FrozenAt != nil || isSubject(p, a) {
		return false
	}
	if p.User.ID == a.RequestedBy {
		d, _ := s.def(a.Kind)
		return d.RequesterMayDecide && a.BrandID != nil && p.HasBrandRole(*a.BrandID, d.RequesterRole)
	}
	if p.User.CompanyRole == store.CompanyOwner || p.User.CompanyRole == store.CompanyApprover {
		return true
	}
	d, _ := s.def(a.Kind)
	return !d.CompanyLevel && a.BrandID != nil && p.Roles[*a.BrandID] == store.RoleBrandAdmin
}

type deciderKey struct{}

// Decider is who is approving, inside a Definition's Execute.
func Decider(ctx context.Context) *auth.Principal {
	p, _ := ctx.Value(deciderKey{}).(*auth.Principal)
	return p
}

// CanSee: deciders, the requester, and admins of the request's brand.
func (s *Service) CanSee(p *auth.Principal, a store.Approval) bool {
	return p.User.ID == a.RequestedBy || p.SeesAllBrands() || s.CanDecide(p, a) ||
		(a.BrandID != nil && p.Roles[*a.BrandID] == store.RoleBrandAdmin)
}

var (
	errNotPending = httpx.Errorf(http.StatusConflict, "not_pending", "this request was already decided, cancelled or has expired")
	errOwn        = httpx.Errorf(http.StatusForbidden, "own_request", "you can't approve or reject your own request")
	errSubject    = httpx.Errorf(http.StatusForbidden, "about_you", "this request is about you, so someone else has to decide it")
)

// Approve runs the action and marks the request approved, atomically.
func (s *Service) Approve(ctx context.Context, p *auth.Principal, id uuid.UUID, note string, audit store.AuditEntry) (store.Approval, error) {
	return s.decide(ctx, p, id, store.ApprovalApproved, note, audit)
}

func (s *Service) Reject(ctx context.Context, p *auth.Principal, id uuid.UUID, note string, audit store.AuditEntry) (store.Approval, error) {
	return s.decide(ctx, p, id, store.ApprovalRejected, note, audit)
}

func (s *Service) decide(ctx context.Context, p *auth.Principal, id uuid.UUID, status store.ApprovalStatus, note string, audit store.AuditEntry) (store.Approval, error) {
	var out store.Approval
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		a, err := tx.LockApproval(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		if !s.CanSee(p, a) {
			return httpx.ErrNotFound
		}
		if a.Status != store.ApprovalPending || !a.ExpiresAt.After(s.now()) {
			return errNotPending
		}
		d, ok := s.def(a.Kind)
		if !ok {
			return fmt.Errorf("%w: %s", ErrUnknown, a.Kind)
		}
		if a.RequestedBy == p.User.ID && !d.RequesterMayDecide {
			return errOwn
		}
		if isSubject(p, a) {
			return errSubject
		}
		if !s.CanDecide(p, a) {
			return httpx.ErrForbidden
		}
		ctx := context.WithValue(ctx, deciderKey{}, p)

		var result any
		switch status {
		case store.ApprovalApproved:
			if d.Execute == nil {
				return httpx.Errorf(http.StatusConflict, "not_ready", "approving %q isn't supported yet", d.Label)
			}
			result, err = d.Execute(ctx, tx, a)
			var rej *RejectError
			switch {
			case errors.As(err, &rej):
				status, note = store.ApprovalRejected, "Couldn't be carried out: "+rej.Reason
				if d.OnReject != nil {
					if err := d.OnReject(ctx, tx, a); err != nil {
						return err
					}
				}
			case err != nil:
				return err
			}
		default:
			if d.OnReject != nil {
				if err := d.OnReject(ctx, tx, a); err != nil {
					return err
				}
			}
		}
		if err := tx.DecideApproval(ctx, a.ID, status, &p.User.ID, note, result); err != nil {
			return err
		}
		audit.Action, audit.TargetType, audit.TargetID, audit.BrandID = "approval."+string(status), "approval", a.ID.String(), a.BrandID
		if err := tx.Audit(ctx, audit, nil, map[string]any{"kind": a.Kind, "summary": a.Summary, "note": note}); err != nil {
			return err
		}
		out, err = tx.ApprovalByID(ctx, a.ID)
		return err
	})
	return out, err
}

// Cancel withdraws your own pending request.
func (s *Service) Cancel(ctx context.Context, p *auth.Principal, id uuid.UUID, audit store.AuditEntry) error {
	return s.store.InTx(ctx, func(tx *store.Store) error {
		a, err := tx.LockApproval(ctx, id)
		if errors.Is(err, store.ErrNotFound) || (err == nil && a.RequestedBy != p.User.ID) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		if a.Status != store.ApprovalPending {
			return errNotPending
		}
		if d, ok := s.def(a.Kind); ok && d.OnReject != nil {
			if err := d.OnReject(ctx, tx, a); err != nil {
				return err
			}
		}
		if err := tx.DecideApproval(ctx, a.ID, store.ApprovalCancelled, nil, "", nil); err != nil {
			return err
		}
		audit.Action, audit.TargetType, audit.TargetID, audit.BrandID = "approval.cancelled", "approval", a.ID.String(), a.BrandID
		return tx.Audit(ctx, audit, nil, map[string]any{"kind": a.Kind, "summary": a.Summary})
	})
}

// Expire closes overdue requests (housekeeping job), running each kind's
// OnReject so e.g. an expired admin invite stops blocking the email.
func (s *Service) Expire(ctx context.Context) (int64, error) {
	ids, err := s.store.OverdueApprovals(ctx)
	if err != nil {
		return 0, err
	}
	var n int64
	for _, id := range ids {
		err := s.store.InTx(ctx, func(tx *store.Store) error {
			a, err := tx.LockApproval(ctx, id)
			if err != nil || a.Status != store.ApprovalPending || a.ExpiresAt.After(s.now()) {
				return err
			}
			if d, ok := s.def(a.Kind); ok && d.OnReject != nil {
				if err := d.OnReject(ctx, tx, a); err != nil {
					return err
				}
			}
			if err := tx.DecideApproval(ctx, a.ID, store.ApprovalExpired, nil, "", nil); err != nil {
				return err
			}
			n++
			return nil
		})
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

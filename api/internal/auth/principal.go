package auth

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// StepUpWindow is how long a passkey confirmation unlocks sensitive actions.
const StepUpWindow = 5 * time.Minute

// Principal is the signed-in user for one request.
type Principal struct {
	User        store.User
	Roles       store.BrandRoles
	SessionHash []byte
	SteppedUpAt *time.Time
}

func (p *Principal) IsOwner() bool { return p.User.CompanyRole == store.CompanyOwner }

// SeesAllBrands: owners and company approvers can view every brand (read-only for approvers).
func (p *Principal) SeesAllBrands() bool {
	return p.User.CompanyRole == store.CompanyOwner || p.User.CompanyRole == store.CompanyApprover
}

func (p *Principal) CanViewBrand(id uuid.UUID) bool {
	return p.SeesAllBrands() || p.Roles[id] != ""
}

// HasBrandRole reports whether the user holds at least min on the brand.
// Owners count as brand admins everywhere.
func (p *Principal) HasBrandRole(id uuid.UUID, min store.BrandRole) bool {
	if p.IsOwner() {
		return true
	}
	return p.Roles[id].Rank() >= min.Rank()
}

func (p *Principal) SteppedUp(now time.Time) bool {
	return p.SteppedUpAt != nil && now.Sub(*p.SteppedUpAt) < StepUpWindow
}

type ctxKey struct{}

func withPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the principal, or nil when nobody is signed in.
func FromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKey{}).(*Principal)
	return p
}

// WithPrincipalForTest lets other packages' tests act as a user.
func WithPrincipalForTest(ctx context.Context, p *Principal) context.Context {
	return withPrincipal(ctx, p)
}

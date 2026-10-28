package billingrenew

import (
	"context"
	"errors"
	"time"

	"github.com/bushixiucai/urmail/internal/payment"
)

// ErrHostedAuthorizationUncertain preserves an ambiguous setup operation for
// read-only reconciliation; callers must never create a second hosted setup.
var ErrHostedAuthorizationUncertain = errors.New("hosted authorization requires read-only reconciliation")

// HostedAuthorizationRequest binds the provider setup to a persisted server
// intent. Return URLs are trusted server values and never browser-supplied.
type HostedAuthorizationRequest struct {
	TenantID       int64
	SubscriptionID int64
	ActorID        int64
	RequestID      string
	Currency       string
	SuccessURL     string
	CancelURL      string
}

// HostedAuthorizationSession identifies one provider-hosted, non-charging setup.
type HostedAuthorizationSession struct {
	Reference string
	URL       string
	ExpiresAt time.Time
}

// HostedAuthorizationAdapter starts explicit hosted consent without enrolling
// or charging a subscription. The existing verifier seals provider facts later.
type HostedAuthorizationAdapter interface {
	StartAuthorization(context.Context, payment.Merchant, HostedAuthorizationRequest) (HostedAuthorizationSession, error)
}

// HostedAuthorizationLookup can recover an acknowledged-lost setup only by GET.
// Empty, truncated or ambiguous observations remain uncertain, never permission
// to POST another setup session.
type HostedAuthorizationLookup interface {
	LookupAuthorization(context.Context, payment.Merchant, HostedAuthorizationRequest) (HostedAuthorizationSession, error)
}

package billing

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	ErrInvalidStatusTransition = errors.New("invalid subscription status transition")
	ErrPlanNotFound            = errors.New("plan not found")
	ErrQuotaExceeded           = errors.New("tenant resource usage exceeds new plan limits")
)

// Valid subscription statuses
const (
	StatusTrialing = "trialing"
	StatusActive   = "active"
	StatusPastDue  = "past_due"
	StatusCanceled = "canceled"
	StatusExpired  = "expired"
)

// Subscription is the billing domain's own representation of a subscription
// row. The domain package must not import internal/store (AGENTS.md layering:
// business logic depends on neither DB nor HTTP), so the DTO lives here and
// the store layer maps its generated types onto it.
type Subscription struct {
	ID                int64
	TenantID          int64
	PlanID            string
	Status            string
	PeriodStart       time.Time
	PeriodEnd         time.Time
	CancelAtPeriodEnd bool
	TrialEndsAt       *time.Time
	CreatedAt         time.Time
}

// CreateSubscriptionParams carries the fields needed to insert a new
// subscription, expressed with domain types only.
type CreateSubscriptionParams struct {
	TenantID          int64
	PlanID            string
	Status            string
	PeriodStart       time.Time
	PeriodEnd         time.Time
	CancelAtPeriodEnd bool
	TrialEndsAt       *time.Time
}

// SubscriptionManager handles atomic subscription changes and plan limit validations.
type SubscriptionManager struct{}

// NewSubscriptionManager creates a subscription manager.
func NewSubscriptionManager() *SubscriptionManager {
	return &SubscriptionManager{}
}

// PlanLimits holds limits defined by a Plan.
type PlanLimits struct {
	Code           string
	QuotaBytes     int64
	MailboxLimit   int32
	DomainLimit    int32
	DailySendLimit int32
}

// ValidateDowngrade checks if current tenant usage fits within target plan limits.
func ValidateDowngrade(limits PlanLimits, currentDomains, currentMailboxes int64, currentStorageBytes int64) error {
	if int64(limits.DomainLimit) > 0 && currentDomains > int64(limits.DomainLimit) {
		return fmt.Errorf("%w: domains (%d) exceeds limit (%d)", ErrQuotaExceeded, currentDomains, limits.DomainLimit)
	}
	if int64(limits.MailboxLimit) > 0 && currentMailboxes > int64(limits.MailboxLimit) {
		return fmt.Errorf("%w: mailboxes (%d) exceeds limit (%d)", ErrQuotaExceeded, currentMailboxes, limits.MailboxLimit)
	}
	if limits.QuotaBytes > 0 && currentStorageBytes > limits.QuotaBytes {
		return fmt.Errorf("%w: storage (%d bytes) exceeds limit (%d bytes)", ErrQuotaExceeded, currentStorageBytes, limits.QuotaBytes)
	}
	return nil
}

// AtomicReplaceContract defines the database operations executed inside a single transaction
// to guarantee at most one active subscription per tenant (subscriptions_one_active_per_tenant).
// It is expressed in domain types; the store adapter (internal/api or a future
// internal/store bridge) maps these onto the sqlc-generated methods.
type AtomicReplaceContract interface {
	CancelActiveSubscriptionsForTenant(ctx context.Context, tenantID int64) error
	CreateSubscription(ctx context.Context, arg CreateSubscriptionParams) (Subscription, error)
}

// AtomicSwitchPlan performs an atomic replacement of the tenant's subscription.
func (sm *SubscriptionManager) AtomicSwitchPlan(
	ctx context.Context,
	exec AtomicReplaceContract,
	tenantID int64,
	planCode string,
	periodStart, periodEnd time.Time,
	trialEndsAt *time.Time,
) (*Subscription, error) {
	// Step 1: cancel any currently active, trialing or past_due subscription
	if err := exec.CancelActiveSubscriptionsForTenant(ctx, tenantID); err != nil {
		return nil, fmt.Errorf("cancel active subscriptions: %w", err)
	}

	// Step 2: insert the new active subscription
	sub, err := exec.CreateSubscription(ctx, CreateSubscriptionParams{
		TenantID:          tenantID,
		PlanID:            planCode,
		Status:            StatusActive,
		PeriodStart:       periodStart,
		PeriodEnd:         periodEnd,
		CancelAtPeriodEnd: false,
		TrialEndsAt:       trialEndsAt,
	})
	if err != nil {
		return nil, fmt.Errorf("create new subscription: %w", err)
	}

	return &sub, nil
}

package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
)

var (
	ErrInvalidStatusTransition = errors.New("invalid subscription status transition")
	ErrPlanNotFound             = errors.New("plan not found")
	ErrQuotaExceeded            = errors.New("tenant resource usage exceeds new plan limits")
)

// Valid subscription statuses
const (
	StatusTrialing = "trialing"
	StatusActive   = "active"
	StatusPastDue  = "past_due"
	StatusCanceled = "canceled"
	StatusExpired  = "expired"
)

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
type AtomicReplaceContract interface {
	CancelActiveSubscriptionsForTenant(ctx context.Context, tenantID int64) error
	CreateSubscription(ctx context.Context, arg store.CreateSubscriptionParams) (store.Subscriptions, error)
}

// AtomicSwitchPlan performs an atomic replacement of the tenant's subscription.
func (sm *SubscriptionManager) AtomicSwitchPlan(
	ctx context.Context,
	exec AtomicReplaceContract,
	tenantID int64,
	planCode string,
	periodStart, periodEnd time.Time,
	trialEndsAt *time.Time,
) (*store.Subscriptions, error) {
	// Step 1: cancel any currently active or trialing subscriptions
	if err := exec.CancelActiveSubscriptionsForTenant(ctx, tenantID); err != nil {
		return nil, fmt.Errorf("cancel active subscriptions: %w", err)
	}

	var trialEnd pgtype.Timestamptz
	if trialEndsAt != nil {
		trialEnd = pgtype.Timestamptz{Time: *trialEndsAt, Valid: true}
	}

	// Step 2: insert the new active subscription
	sub, err := exec.CreateSubscription(ctx, store.CreateSubscriptionParams{
		TenantID:          tenantID,
		PlanID:            planCode,
		Status:            StatusActive,
		PeriodStart:       pgtype.Timestamptz{Time: periodStart, Valid: true},
		PeriodEnd:         pgtype.Timestamptz{Time: periodEnd, Valid: true},
		CancelAtPeriodEnd: false,
		TrialEndsAt:       trialEnd,
	})
	if err != nil {
		return nil, fmt.Errorf("create new subscription: %w", err)
	}

	return &sub, nil
}

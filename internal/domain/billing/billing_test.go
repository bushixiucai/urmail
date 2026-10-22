package billing_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/store"
)

type mockSubExecutor struct {
	canceled bool
	created  bool
}

func (m *mockSubExecutor) CancelActiveSubscriptionsForTenant(ctx context.Context, tenantID int64) error {
	m.canceled = true
	return nil
}

func (m *mockSubExecutor) CreateSubscription(ctx context.Context, arg store.CreateSubscriptionParams) (store.Subscriptions, error) {
	m.created = true
	return store.Subscriptions{
		ID:        1,
		TenantID:  arg.TenantID,
		PlanID:    arg.PlanID,
		Status:    arg.Status,
		CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}, nil
}

func TestAtomicSwitchPlan(t *testing.T) {
	sm := billing.NewSubscriptionManager()
	exec := &mockSubExecutor{}

	start := time.Now()
	end := start.Add(30 * 24 * time.Hour)

	sub, err := sm.AtomicSwitchPlan(context.Background(), exec, 42, "pro", start, end, nil)
	if err != nil {
		t.Fatalf("AtomicSwitchPlan failed: %v", err)
	}

	if !exec.canceled {
		t.Error("expected CancelActiveSubscriptionsForTenant to be called")
	}
	if !exec.created {
		t.Error("expected CreateSubscription to be called")
	}
	if sub.PlanID != "pro" || sub.Status != billing.StatusActive {
		t.Errorf("unexpected subscription: %+v", sub)
	}
}

func TestValidateDowngrade(t *testing.T) {
	limits := billing.PlanLimits{
		Code:         "basic",
		DomainLimit:  2,
		MailboxLimit: 10,
		QuotaBytes:   1000,
	}

	if err := billing.ValidateDowngrade(limits, 1, 5, 500); err != nil {
		t.Errorf("expected allowed downgrade, got %v", err)
	}

	if err := billing.ValidateDowngrade(limits, 3, 5, 500); err == nil {
		t.Error("expected domain limit exceed error, got nil")
	}

	if err := billing.ValidateDowngrade(limits, 1, 15, 500); err == nil {
		t.Error("expected mailbox limit exceed error, got nil")
	}

	if err := billing.ValidateDowngrade(limits, 1, 5, 2000); err == nil {
		t.Error("expected storage limit exceed error, got nil")
	}
}

func TestApplyDiscount(t *testing.T) {
	orig := int64(10000) // 100.00 CNY

	// Fixed discount of 20.00 CNY
	if got := billing.ApplyDiscount(orig, "fixed", 2000); got != 8000 {
		t.Errorf("expected 8000, got %d", got)
	}

	// Percent discount of 15%
	if got := billing.ApplyDiscount(orig, "percent", 15); got != 8500 {
		t.Errorf("expected 8500, got %d", got)
	}

	// 100% discount
	if got := billing.ApplyDiscount(orig, "percent", 100); got != 0 {
		t.Errorf("expected 0, got %d", got)
	}
}

func TestCanTransitionOrder(t *testing.T) {
	if !billing.CanTransitionOrder(billing.OrderStatusPending, billing.OrderStatusPaid) {
		t.Error("pending -> paid should be allowed")
	}
	if !billing.CanTransitionOrder(billing.OrderStatusPaid, billing.OrderStatusRefunded) {
		t.Error("paid -> refunded should be allowed")
	}
	if billing.CanTransitionOrder(billing.OrderStatusFailed, billing.OrderStatusPaid) {
		t.Error("failed -> paid should not be allowed")
	}
}

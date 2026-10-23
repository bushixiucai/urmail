package billing_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/domain/billing"
)

type mockSubExecutor struct {
	canceled bool
	created  bool
}

func (m *mockSubExecutor) CancelActiveSubscriptionsForTenant(_ context.Context, _ int64) error {
	m.canceled = true
	return nil
}

func (m *mockSubExecutor) CreateSubscription(_ context.Context, arg billing.CreateSubscriptionParams) (billing.Subscription, error) {
	m.created = true
	return billing.Subscription{
		ID:        1,
		TenantID:  arg.TenantID,
		PlanID:    arg.PlanID,
		Status:    arg.Status,
		CreatedAt: time.Now(),
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

// The domain contract must not leak store types: it is expressed purely with
// billing-domain DTOs.
func TestAtomicReplaceContractUsesDomainTypes(_ *testing.T) {
	var exec billing.AtomicReplaceContract = &mockSubExecutor{}
	_ = exec
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
	if got, err := billing.ApplyDiscount(orig, "fixed", 2000); err != nil || got != 8000 {
		t.Errorf("expected 8000, got %d (err=%v)", got, err)
	}

	// Percent discount of 15%
	if got, err := billing.ApplyDiscount(orig, "percent", 15); err != nil || got != 8500 {
		t.Errorf("expected 8500, got %d (err=%v)", got, err)
	}

	// 100% discount
	if got, err := billing.ApplyDiscount(orig, "percent", 100); err != nil || got != 0 {
		t.Errorf("expected 0, got %d (err=%v)", got, err)
	}

	// fixed coupon with value <= 0 must be rejected, not silently free
	if _, err := billing.ApplyDiscount(orig, "fixed", 0); !errors.Is(err, billing.ErrInvalidCoupon) {
		t.Errorf("expected ErrInvalidCoupon for fixed value 0, got %v", err)
	}
	if _, err := billing.ApplyDiscount(orig, "fixed", -5); !errors.Is(err, billing.ErrInvalidCoupon) {
		t.Errorf("expected ErrInvalidCoupon for fixed value -5, got %v", err)
	}

	// percent coupon outside (0, 100] must be rejected
	if _, err := billing.ApplyDiscount(orig, "percent", 0); !errors.Is(err, billing.ErrInvalidCoupon) {
		t.Errorf("expected ErrInvalidCoupon for percent value 0, got %v", err)
	}
	if _, err := billing.ApplyDiscount(orig, "percent", 150); !errors.Is(err, billing.ErrInvalidCoupon) {
		t.Errorf("expected ErrInvalidCoupon for percent value 150, got %v", err)
	}

	// trial_days coupon has no monetary effect
	if got, err := billing.ApplyDiscount(orig, "trial_days", 14); err != nil || got != orig {
		t.Errorf("expected trial_days to leave amount at %d, got %d (err=%v)", orig, got, err)
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

func TestInvoiceLifecycle(t *testing.T) {
	now := time.Now()
	p := billing.CreateInvoiceParams{
		TenantID:    7,
		Number:      billing.GenerateInvoiceNumber(7, now),
		PeriodStart: &now,
		PeriodEnd:   nil,
		AmountCents: 9900,
		Currency:    "CNY",
	}
	if err := billing.ValidateNewInvoice(p); err != nil {
		t.Fatalf("ValidateNewInvoice: %v", err)
	}
	if len(p.Number) == 0 || p.Number[:4] != "INV-" {
		t.Errorf("unexpected invoice number %q", p.Number)
	}

	inv := &billing.Invoice{Status: billing.InvoiceStatusIssued, AmountCents: 9900}
	if err := inv.MarkInvoicePaid(); err != nil {
		t.Fatalf("MarkInvoicePaid: %v", err)
	}
	if inv.Status != billing.InvoiceStatusPaid || inv.IssuedAt == nil {
		t.Errorf("unexpected invoice after mark paid: %+v", inv)
	}
	if err := inv.MarkInvoicePaid(); !errors.Is(err, billing.ErrInvalidInvoiceStatus) {
		t.Errorf("paid -> paid must be rejected, got %v", err)
	}
	voided := &billing.Invoice{Status: billing.InvoiceStatusDraft}
	if !billing.CanTransitionInvoiceStatus(voided.Status, billing.InvoiceStatusVoid) {
		t.Error("draft -> void should be allowed")
	}
	if billing.CanTransitionInvoiceStatus(billing.InvoiceStatusVoid, billing.InvoiceStatusPaid) {
		t.Error("void -> paid must be rejected")
	}
}

func TestValidateNewInvoiceRejectsBadInput(t *testing.T) {
	if err := billing.ValidateNewInvoice(billing.CreateInvoiceParams{TenantID: 1, Number: "N", AmountCents: 0, Currency: ""}); err == nil {
		t.Error("missing currency must be rejected")
	}
	if err := billing.ValidateNewInvoice(billing.CreateInvoiceParams{TenantID: 0, Number: "N", AmountCents: 100, Currency: "CNY"}); err == nil {
		t.Error("missing tenant must be rejected")
	}
	bad := time.Now()
	worse := bad.Add(-time.Hour)
	if err := billing.ValidateNewInvoice(billing.CreateInvoiceParams{TenantID: 1, Number: "N", AmountCents: 100, Currency: "CNY", PeriodStart: &bad, PeriodEnd: &worse}); err == nil {
		t.Error("period_end before period_start must be rejected")
	}
}

func TestZeroPlanLimitsDenyExistingResources(t *testing.T) {
	for _, usage := range []struct{ domains, mailboxes, bytes int64 }{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}} {
		if billing.ValidateDowngrade(billing.PlanLimits{}, usage.domains, usage.mailboxes, usage.bytes) == nil {
			t.Fatal("zero limit accepted existing resource")
		}
	}
	if err := billing.ValidateDowngrade(billing.PlanLimits{DomainLimit: -1, MailboxLimit: -1, QuotaBytes: -1}, 10, 10, 1000); err != nil {
		t.Fatal(err)
	}
}
func TestDiscountRejectsInvalidAndHandlesLargeAmount(t *testing.T) {
	if _, err := billing.ApplyDiscount(100, "unknown", 1); err == nil {
		t.Fatal("unknown discount accepted")
	}
	if _, err := billing.ApplyDiscount(-1, "fixed", 1); err == nil {
		t.Fatal("negative amount accepted")
	}
	const maximumAmount int64 = 1<<63 - 1
	if got, err := billing.ApplyDiscount(maximumAmount, "percent", 50); err != nil || got <= 0 || got >= maximumAmount {
		t.Fatalf("large discount: %d %v", got, err)
	}
}

package billing_test

import (
	"errors"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/domain/billing"
)

func TestValidateCoupon(t *testing.T) {
	now := time.Date(2026, 10, 23, 14, 0, 0, 0, time.UTC)
	past := now.Add(-24 * time.Hour)
	future := now.Add(24 * time.Hour)
	maxRedemptions := int32(10)

	baseCoupon := billing.CouponInfo{
		Code:           "TEST10",
		Kind:           billing.CouponKindPercent,
		Value:          10,
		MaxRedemptions: &maxRedemptions,
		RedeemedCount:  0,
		ValidFrom:      &past,
		ValidTo:        &future,
		Active:         true,
	}

	t.Run("valid coupon passes", func(t *testing.T) {
		if err := billing.ValidateCoupon(baseCoupon, now); err != nil {
			t.Fatalf("expected valid coupon, got %v", err)
		}
	})

	t.Run("inactive coupon fails", func(t *testing.T) {
		c := baseCoupon
		c.Active = false
		if err := billing.ValidateCoupon(c, now); !errors.Is(err, billing.ErrCouponInactive) {
			t.Errorf("expected ErrCouponInactive, got %v", err)
		}
	})

	t.Run("expired coupon fails", func(t *testing.T) {
		c := baseCoupon
		c.ValidTo = &past
		if err := billing.ValidateCoupon(c, now); !errors.Is(err, billing.ErrCouponExpired) {
			t.Errorf("expected ErrCouponExpired, got %v", err)
		}
	})

	t.Run("not started coupon fails", func(t *testing.T) {
		c := baseCoupon
		c.ValidFrom = &future
		if err := billing.ValidateCoupon(c, now); !errors.Is(err, billing.ErrCouponNotStarted) {
			t.Errorf("expected ErrCouponNotStarted, got %v", err)
		}
	})

	t.Run("exhausted coupon fails", func(t *testing.T) {
		c := baseCoupon
		c.RedeemedCount = 10
		if err := billing.ValidateCoupon(c, now); !errors.Is(err, billing.ErrCouponExhausted) {
			t.Errorf("expected ErrCouponExhausted, got %v", err)
		}
	})
}

func TestCalculateDiscountAndPayable(t *testing.T) {
	t.Run("no coupon", func(t *testing.T) {
		discount, payable, zero, err := billing.CalculateDiscountAndPayable(10000, "", 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if discount != 0 || payable != 10000 || zero {
			t.Errorf("unexpected: discount=%d payable=%d zero=%v", discount, payable, zero)
		}
	})

	t.Run("percent coupon", func(t *testing.T) {
		discount, payable, zero, err := billing.CalculateDiscountAndPayable(10000, billing.CouponKindPercent, 20)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if discount != 2000 || payable != 8000 || zero {
			t.Errorf("unexpected: discount=%d payable=%d zero=%v", discount, payable, zero)
		}
	})

	t.Run("100 percent coupon yields zero amount", func(t *testing.T) {
		discount, payable, zero, err := billing.CalculateDiscountAndPayable(10000, billing.CouponKindPercent, 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if discount != 10000 || payable != 0 || !zero {
			t.Errorf("unexpected: discount=%d payable=%d zero=%v", discount, payable, zero)
		}
	})

	t.Run("fixed coupon", func(t *testing.T) {
		discount, payable, zero, err := billing.CalculateDiscountAndPayable(10000, billing.CouponKindFixed, 3000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if discount != 3000 || payable != 7000 || zero {
			t.Errorf("unexpected: discount=%d payable=%d zero=%v", discount, payable, zero)
		}
	})
}

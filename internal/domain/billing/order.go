package billing

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidOrderStatus = errors.New("invalid order status transition")
)

const (
	OrderKindNew       = "new"
	OrderKindRenew     = "renew"
	OrderKindUpgrade   = "upgrade"
	OrderKindDowngrade = "downgrade"
	OrderKindManual    = "manual"

	OrderStatusPending           = "pending"
	OrderStatusPaid              = "paid"
	OrderStatusFailed            = "failed"
	OrderStatusRefunded          = "refunded"
	OrderStatusPartiallyRefunded = "partially_refunded"
	OrderStatusCanceled          = "canceled"
)

// CanTransitionOrder checks if the transition from current to next is allowed.
func CanTransitionOrder(current, next string) bool {
	switch current {
	case OrderStatusPending:
		return next == OrderStatusPaid || next == OrderStatusFailed || next == OrderStatusCanceled
	case OrderStatusPaid:
		return next == OrderStatusRefunded || next == OrderStatusPartiallyRefunded
	default:
		return false
	}
}

// Coupon kinds as stored in coupons.kind.
const (
	CouponKindFixed     = "fixed"
	CouponKindPercent   = "percent"
	CouponKindTrialDays = "trial_days"
)

// ErrInvalidCoupon indicates a coupon kind/value combination that must never
// reach the price calculation.
var ErrInvalidCoupon = errors.New("invalid coupon")

// ApplyDiscount calculates the discounted amount in cents given a coupon kind
// and value. trial_days coupons grant a trial period instead of a monetary
// discount, so the amount is returned unchanged. A fixed coupon with a
// non-positive value (or a percent coupon outside (0, 100]) is rejected
// instead of silently producing a full discount (M3 review P3).
func ApplyDiscount(originalCents int64, couponKind string, couponValue int64) (int64, error) {
	switch couponKind {
	case CouponKindFixed:
		if couponValue <= 0 {
			return 0, fmt.Errorf("%w: fixed coupon value must be > 0, got %d", ErrInvalidCoupon, couponValue)
		}
		discounted := originalCents - couponValue
		if discounted < 0 {
			return 0, nil
		}
		return discounted, nil
	case CouponKindPercent:
		if couponValue <= 0 || couponValue > 100 {
			return 0, fmt.Errorf("%w: percent coupon value must be in (0, 100], got %d", ErrInvalidCoupon, couponValue)
		}
		if couponValue == 100 {
			return 0, nil
		}
		discount := (originalCents * couponValue) / 100
		return originalCents - discount, nil
	case CouponKindTrialDays:
		// No monetary effect: the trial extension is handled by the caller.
		return originalCents, nil
	default:
		return originalCents, nil
	}
}

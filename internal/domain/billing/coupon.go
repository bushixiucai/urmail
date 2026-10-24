package billing

import (
	"errors"
	"time"
)

// Coupon validation errors.
var (
	ErrCouponInactive   = errors.New("coupon is inactive")
	ErrCouponExpired    = errors.New("coupon has expired")
	ErrCouponNotStarted = errors.New("coupon is not yet valid")
	ErrCouponExhausted  = errors.New("coupon redemption limit reached")
)

// CouponInfo holds the domain view of a coupon.
type CouponInfo struct {
	Code           string
	Kind           string
	Value          int64
	MaxRedemptions *int32
	RedeemedCount  int32
	ValidFrom      *time.Time
	ValidTo        *time.Time
	Active         bool
}

// ValidateCoupon checks whether a coupon is eligible for redemption at the given time.
func ValidateCoupon(coupon CouponInfo, now time.Time) error {
	if !coupon.Active {
		return ErrCouponInactive
	}
	if now.IsZero() {
		now = time.Now()
	}
	if coupon.ValidFrom != nil && now.Before(*coupon.ValidFrom) {
		return ErrCouponNotStarted
	}
	if coupon.ValidTo != nil && now.After(*coupon.ValidTo) {
		return ErrCouponExpired
	}
	if coupon.MaxRedemptions != nil && coupon.RedeemedCount >= *coupon.MaxRedemptions {
		return ErrCouponExhausted
	}
	return nil
}

// CalculateDiscountAndPayable computes the discount and final payable cents.
// It also identifies if this results in a 100% zero-amount order.
func CalculateDiscountAndPayable(originalCents int64, kind string, value int64) (discountCents int64, payableCents int64, isZeroAmount bool, err error) {
	if originalCents < 0 {
		return 0, 0, false, ErrInvalidCoupon
	}
	if kind == "" || value <= 0 {
		return 0, originalCents, false, nil
	}
	discounted, err := ApplyDiscount(originalCents, kind, value)
	if err != nil {
		return 0, 0, false, err
	}
	discount := originalCents - discounted
	payable := discounted
	return discount, payable, payable == 0, nil
}

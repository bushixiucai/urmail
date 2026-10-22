package billing

import (
	"errors"
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

// ApplyDiscount calculates the discounted amount in cents given a coupon kind and value.
func ApplyDiscount(originalCents int64, couponKind string, couponValue int64) int64 {
	switch couponKind {
	case "fixed":
		discounted := originalCents - couponValue
		if discounted < 0 {
			return 0
		}
		return discounted
	case "percent":
		// couponValue is percentage, e.g. 20 for 20% off
		if couponValue >= 100 {
			return 0
		}
		discount := (originalCents * couponValue) / 100
		return originalCents - discount
	default:
		return originalCents
	}
}

package distribution

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidBPS = errors.New("commission bps must be between 0 and 10000")
)

// CalculateCommission calculates the commission in cents from order amount in cents and basis points (bps, 1 bps = 0.01%).
func CalculateCommission(orderAmountCents int64, bps int32) (int64, error) {
	if bps < 0 || bps > 10000 {
		return 0, fmt.Errorf("%w: got %d", ErrInvalidBPS, bps)
	}
	if orderAmountCents <= 0 || bps == 0 {
		return 0, nil
	}

	// amount * bps / 10000
	return (orderAmountCents * int64(bps)) / 10000, nil
}

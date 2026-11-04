package db

import (
	"context"
	"time"
)

// OpenBackgroundPlatform is the compatibility entrypoint for marketing work.
// It uses the same PostgreSQL authority with an independent small physical pool.
func OpenBackgroundPlatform(ctx context.Context, connString string) (*Pool, error) {
	return OpenBudgeted(ctx, connString, RolePlatform, "marketing", Budget{MaxConns: 2, MaxQueue: 4, AcquireTimeout: 250 * time.Millisecond, OperationTimeout: 30 * time.Second})
}

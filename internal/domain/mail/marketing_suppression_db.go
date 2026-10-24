package mail

import (
	"context"
	"strings"

	"github.com/bushixiucai/urmail/internal/store"
)

// SuppressionChecker performs a tenant-scoped durable recipient check.
type SuppressionChecker interface {
	IsSuppressed(context.Context, int64, string) (bool, error)
}

// PersistentSuppressionStore is the minimal tenant-scoped DB contract needed by senders.
type PersistentSuppressionStore interface {
	CheckMarketingSuppression(context.Context, store.CheckMarketingSuppressionParams) (bool, error)
}

type persistentSuppressionChecker struct{ store PersistentSuppressionStore }

// NewPersistentSuppressionChecker wraps SQLC queries for use by dispatchers.
func NewPersistentSuppressionChecker(q PersistentSuppressionStore) SuppressionChecker {
	return persistentSuppressionChecker{store: q}
}

func (p persistentSuppressionChecker) IsSuppressed(ctx context.Context, tenantID int64, email string) (bool, error) {
	return CheckPersistentSuppression(ctx, p.store, tenantID, email)
}

// CheckPersistentSuppression checks the durable tenant suppression list. A DB failure is returned,
// not treated as an absent suppression, so dispatch callers can fail closed.
func CheckPersistentSuppression(ctx context.Context, q PersistentSuppressionStore, tenantID int64, email string) (bool, error) {
	if q == nil || tenantID <= 0 {
		return false, ErrSuppressionManagerRequired
	}
	return q.CheckMarketingSuppression(ctx, store.CheckMarketingSuppressionParams{TenantID: tenantID, Email: strings.ToLower(strings.TrimSpace(email))})
}

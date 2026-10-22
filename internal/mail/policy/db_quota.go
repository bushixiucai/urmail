package policy

import (
	"context"
	"fmt"

	"github.com/bushixiucai/urmail/internal/store"
)

// UsageStore abstracts queries needed to check and record daily mailbox sending count.
type UsageStore interface {
	GetMailboxDailySent(ctx context.Context, address string) (store.GetMailboxDailySentRow, error)
	GetMailboxDailySentForUpdate(ctx context.Context, address string) (store.GetMailboxDailySentForUpdateRow, error)
	IncrementMailboxDailySent(ctx context.Context, arg store.IncrementMailboxDailySentParams) error
}

// DBQuotaChecker implements QuotaChecker against the PostgreSQL store.
type DBQuotaChecker struct {
	Store         UsageStore
	DailyLimit    int32 // Daily message limit per mailbox
	UseLock       bool  // Whether to acquire FOR UPDATE lock
}

// NewDBQuotaChecker creates a DB-backed QuotaChecker.
func NewDBQuotaChecker(s UsageStore, dailyLimit int32) *DBQuotaChecker {
	return &DBQuotaChecker{
		Store:      s,
		DailyLimit: dailyLimit,
		UseLock:    false,
	}
}

// NewLockingDBQuotaChecker creates a DB-backed QuotaChecker that locks the row during check.
func NewLockingDBQuotaChecker(s UsageStore, dailyLimit int32) *DBQuotaChecker {
	return &DBQuotaChecker{
		Store:      s,
		DailyLimit: dailyLimit,
		UseLock:    true,
	}
}

// CheckAndIncrement verifies if the sender mailbox has not exceeded the daily sending limit,
// and increments the day's usage counter if within limit.
func (c *DBQuotaChecker) CheckAndIncrement(ctx context.Context, mailboxAddress string) (bool, error) {
	var messagesSent int32
	var mailboxID, tenantID int64

	if c.UseLock {
		row, err := c.Store.GetMailboxDailySentForUpdate(ctx, mailboxAddress)
		if err != nil {
			return false, fmt.Errorf("query mailbox daily sent (for update): %w", err)
		}
		messagesSent = row.MessagesSent
		mailboxID = row.MailboxID
		tenantID = row.TenantID
	} else {
		row, err := c.Store.GetMailboxDailySent(ctx, mailboxAddress)
		if err != nil {
			return false, fmt.Errorf("query mailbox daily sent: %w", err)
		}
		messagesSent = row.MessagesSent
		mailboxID = row.MailboxID
		tenantID = row.TenantID
	}

	if c.DailyLimit > 0 && messagesSent >= c.DailyLimit {
		return false, nil
	}

	if err := c.Store.IncrementMailboxDailySent(ctx, store.IncrementMailboxDailySentParams{
		MailboxID: mailboxID,
		TenantID:  tenantID,
	}); err != nil {
		return false, fmt.Errorf("increment daily sent: %w", err)
	}

	return true, nil
}

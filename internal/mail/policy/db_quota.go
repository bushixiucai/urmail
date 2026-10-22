package policy

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/jackc/pgx/v5"
)

// UsageStore abstracts the lookups needed to resolve a mailbox address to its
// usage row (mailbox_id / tenant_id) and current counters.
type UsageStore interface {
	GetMailboxDailySent(ctx context.Context, address string) (store.GetMailboxDailySentRow, error)
	GetMailboxDailySentForUpdate(ctx context.Context, address string) (store.GetMailboxDailySentForUpdateRow, error)

	// IncrementMailboxDailySent is only used by the legacy (non-atomic)
	// path, i.e. when Raw is nil. It returns the new counter value; a
	// pgx.ErrNoRows means the limit was already reached (guarded UPSERT).
	IncrementMailboxDailySent(ctx context.Context, arg store.IncrementMailboxDailySentParams) (int32, error)
}

// RawQuerier executes raw SQL. It is satisfied by *store.Queries and by
// pgxpool.Pool / pgx.Tx alike.
type RawQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row
}

// atomicIncrementSQL performs the check-and-increment as a single atomic
// statement: the counter is only advanced while it stays below the limit, so
// concurrent policy workers cannot oversell the daily quota (M2 review P2).
// ErrNoRows from this statement means the limit was already reached.
const atomicIncrementSQL = `INSERT INTO mailbox_usage (mailbox_id, tenant_id, day, messages_sent)
VALUES ($1, $2, CURRENT_DATE, 1)
ON CONFLICT (mailbox_id, day)
DO UPDATE SET messages_sent = mailbox_usage.messages_sent + 1
WHERE mailbox_usage.messages_sent < $3
RETURNING messages_sent`

// DBQuotaChecker implements QuotaChecker against the PostgreSQL store.
type DBQuotaChecker struct {
	Store UsageStore
	// Raw, when set, is used for the atomic check-and-increment UPSERT.
	// When nil the legacy check-then-increment path is used (racy under
	// concurrent policy workers; kept for backward compatibility).
	Raw        RawQuerier
	DailyLimit int32 // Daily message limit per mailbox
	UseLock    bool  // Whether to acquire FOR UPDATE lock
}

// NewDBQuotaChecker creates a DB-backed QuotaChecker using the legacy
// check-then-increment path.
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

// NewAtomicDBQuotaChecker creates a DB-backed QuotaChecker whose
// check-and-increment is a single guarded UPSERT, safe under concurrency.
func NewAtomicDBQuotaChecker(s UsageStore, raw RawQuerier, dailyLimit int32) *DBQuotaChecker {
	return &DBQuotaChecker{
		Store:      s,
		Raw:        raw,
		DailyLimit: dailyLimit,
		UseLock:    false,
	}
}

// CheckAndIncrement verifies if the sender mailbox has not exceeded the daily
// sending limit, and increments the day's usage counter if within limit.
func (c *DBQuotaChecker) CheckAndIncrement(ctx context.Context, mailboxAddress string) (bool, error) {
	var messagesSent int32
	var mailboxID, tenantID int64
	limit := c.DailyLimit

	if c.UseLock {
		row, err := c.Store.GetMailboxDailySentForUpdate(ctx, mailboxAddress)
		if err != nil {
			return false, fmt.Errorf("query mailbox daily sent (for update): %w", err)
		}
		messagesSent = row.MessagesSent
		mailboxID = row.MailboxID
		tenantID = row.TenantID
		if row.DailySendLimit > 0 {
			limit = row.DailySendLimit
		}
	} else {
		row, err := c.Store.GetMailboxDailySent(ctx, mailboxAddress)
		if err != nil {
			return false, fmt.Errorf("query mailbox daily sent: %w", err)
		}
		messagesSent = row.MessagesSent
		mailboxID = row.MailboxID
		tenantID = row.TenantID
		if row.DailySendLimit > 0 {
			limit = row.DailySendLimit
		}
	}

	if limit > 0 && messagesSent >= limit {
		return false, nil
	}

	// DailyLimit <= 0 (and no plan limit) means unlimited: pass the largest
	// counter value so the guarded UPSERT stays permissive.
	atomicLimit := limit
	if atomicLimit <= 0 {
		atomicLimit = math.MaxInt32
	}

	if c.Raw != nil {
		// Atomic path: a single guarded UPSERT decides both the check and
		// the increment. ErrNoRows means the guarded UPDATE did not fire,
		// i.e. the quota was exhausted by a concurrent worker in between.
		var sent int32
		err := c.Raw.QueryRow(ctx, atomicIncrementSQL, mailboxID, tenantID, atomicLimit).Scan(&sent)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("atomic increment daily sent: %w", err)
		}
		return true, nil
	}

	// The generated query is a guarded UPSERT: it refuses to increment past
	// $3.
	limit = atomicLimit
	if _, err := c.Store.IncrementMailboxDailySent(ctx, store.IncrementMailboxDailySentParams{
		MailboxID:    mailboxID,
		TenantID:     tenantID,
		MessagesSent: limit,
	}); err != nil {
		return false, fmt.Errorf("increment daily sent: %w", err)
	}

	return true, nil
}

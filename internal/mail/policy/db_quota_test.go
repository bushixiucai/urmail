package policy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/mail/policy"
	"github.com/bushixiucai/urmail/internal/store"
)

// fakeRawQuerier records raw queries for the atomic UPSERT path.
type fakeRawQuerier struct {
	err        error
	sentAfter  int32
	limitSeen  int32
	queryCount int
}

func (f *fakeRawQuerier) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	f.queryCount++
	return &fakeRow{err: f.err, sent: f.sentAfter}
}

type fakeRow struct {
	err  error
	sent int32
}

func (r *fakeRow) Scan(dest ...interface{}) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) > 0 {
		if p, ok := dest[0].(*int32); ok {
			*p = r.sent
		}
	}
	return nil
}

type mockUsageStore struct {
	sentCount   int32
	incremented bool
	locked      bool
}

func (m *mockUsageStore) GetMailboxDailySent(ctx context.Context, address string) (store.GetMailboxDailySentRow, error) {
	return store.GetMailboxDailySentRow{
		MessagesSent: m.sentCount,
		MailboxID:    101,
		TenantID:     1,
	}, nil
}

func (m *mockUsageStore) GetMailboxDailySentForUpdate(ctx context.Context, address string) (store.GetMailboxDailySentForUpdateRow, error) {
	m.locked = true
	return store.GetMailboxDailySentForUpdateRow{
		MessagesSent: m.sentCount,
		MailboxID:    101,
		TenantID:     1,
	}, nil
}

func (m *mockUsageStore) IncrementMailboxDailySent(ctx context.Context, arg store.IncrementMailboxDailySentParams) error {
	m.incremented = true
	m.sentCount++
	return nil
}

func TestDBQuotaChecker(t *testing.T) {
	ctx := context.Background()

	t.Run("within_limit_standard", func(t *testing.T) {
		mock := &mockUsageStore{sentCount: 10}
		checker := policy.NewDBQuotaChecker(mock, 100)

		allowed, err := checker.CheckAndIncrement(ctx, "user@example.com")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !allowed {
			t.Error("expected allowed=true")
		}
		if !mock.incremented {
			t.Error("expected incremented=true")
		}
		if mock.locked {
			t.Error("expected no lock for standard checker")
		}
	})

	t.Run("within_limit_with_lock", func(t *testing.T) {
		mock := &mockUsageStore{sentCount: 10}
		checker := policy.NewLockingDBQuotaChecker(mock, 100)

		allowed, err := checker.CheckAndIncrement(ctx, "user@example.com")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !allowed {
			t.Error("expected allowed=true")
		}
		if !mock.incremented {
			t.Error("expected incremented=true")
		}
		if !mock.locked {
			t.Error("expected lock for locking checker")
		}
	})

	t.Run("limit_reached", func(t *testing.T) {
		mock := &mockUsageStore{sentCount: 100}
		checker := policy.NewDBQuotaChecker(mock, 100)

		allowed, err := checker.CheckAndIncrement(ctx, "user@example.com")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if allowed {
			t.Error("expected allowed=false when limit reached")
		}
		if mock.incremented {
			t.Error("should not increment when limit reached")
		}
	})
}
func TestDBQuotaCheckerAtomic(t *testing.T) {
	ctx := context.Background()

	t.Run("atomic_increment_allowed", func(t *testing.T) {
		fake := &fakeRawQuerier{sentAfter: 11}
		mock := &mockUsageStore{sentCount: 10}
		checker := policy.NewAtomicDBQuotaChecker(mock, fake, 100)

		allowed, err := checker.CheckAndIncrement(ctx, "user@example.com")
		if err != nil || !allowed {
			t.Fatalf("expected allowed=true, got allowed=%v err=%v", allowed, err)
		}
		if fake.queryCount != 1 {
			t.Fatalf("expected exactly 1 atomic UPSERT, got %d", fake.queryCount)
		}
	})

	t.Run("atomic_quota_exhausted_concurrently", func(t *testing.T) {
		// ErrNoRows from the guarded UPSERT means a concurrent worker
		// consumed the last slot: must report allowed=false, not an error.
		fake := &fakeRawQuerier{err: pgx.ErrNoRows}
		mock := &mockUsageStore{sentCount: 99}
		checker := policy.NewAtomicDBQuotaChecker(mock, fake, 100)

		allowed, err := checker.CheckAndIncrement(ctx, "user@example.com")
		if err != nil {
			t.Fatalf("ErrNoRows must not surface as error: %v", err)
		}
		if allowed {
			t.Fatal("expected allowed=false when guarded update does not fire")
		}
	})

	t.Run("atomic_db_error_defers", func(t *testing.T) {
		fake := &fakeRawQuerier{err: errors.New("boom")}
		mock := &mockUsageStore{sentCount: 10}
		checker := policy.NewAtomicDBQuotaChecker(mock, fake, 100)

		_, err := checker.CheckAndIncrement(ctx, "user@example.com")
		if err == nil {
			t.Fatal("expected error from raw query failure")
		}
	})

	t.Run("fast_fail_before_upsert", func(t *testing.T) {
		fake := &fakeRawQuerier{sentAfter: 101}
		mock := &mockUsageStore{sentCount: 100}
		checker := policy.NewAtomicDBQuotaChecker(mock, fake, 100)

		allowed, err := checker.CheckAndIncrement(ctx, "user@example.com")
		if err != nil || allowed {
			t.Fatalf("expected allowed=false, got allowed=%v err=%v", allowed, err)
		}
		if fake.queryCount != 0 {
			t.Fatalf("limit reached must not hit the DB, got %d queries", fake.queryCount)
		}
	})
}

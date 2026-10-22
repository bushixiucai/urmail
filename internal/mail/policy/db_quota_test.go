package policy_test

import (
	"context"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/policy"
	"github.com/bushixiucai/urmail/internal/store"
)

type mockUsageStore struct {
	sentCount int32
	incremented bool
	locked bool
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

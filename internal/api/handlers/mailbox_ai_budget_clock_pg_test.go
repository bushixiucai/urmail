package handlers

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
)

func TestAIProviderBudgetAdmissionClockSurvivesOldTransactionDayAndMonthPG(t *testing.T) {
	for _, boundary := range []string{"day", "month"} {
		for _, query := range []string{"metered", "legacy"} {
			t.Run(boundary+"_"+query, func(t *testing.T) {
				pool, principal, _, _, _, _ := providerBudgetFixture(t, "platform", "classify")
				tx, err := pool.PlatformTx(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback(t.Context()) }()
				q := store.New(tx)
				if err = q.PlatformLockBillingTenant(t.Context(), principal.TenantID); err != nil {
					t.Fatal(err)
				}
				var oldClock time.Time
				if err = tx.QueryRow(t.Context(), "SELECT transaction_timestamp()").Scan(&oldClock); err != nil {
					t.Fatal(err)
				}
				// Simulate acquiring the tenant lock after a day/month rollover. No host
				// clock, database clock or production record is changed by this fixture.
				admitted := time.Date(oldClock.Year(), oldClock.Month(), oldClock.Day()+1, 0, 0, 1, 0, oldClock.Location())
				if boundary == "month" {
					admitted = time.Date(oldClock.Year(), oldClock.Month()+1, 1, 0, 0, 1, 0, oldClock.Location())
				}
				instant := pgtype.Timestamptz{Time: admitted, Valid: true}
				var row store.AiUsageRequests
				if query == "metered" {
					row, err = q.PlatformReserveAIRequest(t.Context(), store.PlatformReserveAIRequestParams{TenantID: principal.TenantID, MailboxID: principal.MailboxID, RequestKey: "clock-fixture-metered-key", InputDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PeriodStart: pgtype.Timestamptz{Time: oldClock.Add(-time.Hour), Valid: true}, PeriodEnd: pgtype.Timestamptz{Time: admitted.Add(24 * time.Hour), Valid: true}, Mode: "platform", Provider: "openai", Model: "clock-fixture", CostCurrency: "CNY", ReservedTokens: 100, ReservedCostMicros: 0, CallLimit: 10, MailboxCallLimit: 10, ConcurrencyLimit: 10, TokenLimit: 1000, CostLimit: 1000, AccountedAt: instant})
				} else {
					row, err = q.PlatformReserveMailboxAIRequest(t.Context(), store.PlatformReserveMailboxAIRequestParams{TenantID: principal.TenantID, MailboxID: principal.MailboxID, RequestKey: "clock-fixture-legacy-key", InputDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", PeriodStart: pgtype.Timestamptz{Time: oldClock.Add(-time.Hour), Valid: true}, PeriodEnd: pgtype.Timestamptz{Time: admitted.Add(24 * time.Hour), Valid: true}, Mode: "platform", Provider: "openai", Model: "clock-fixture", CostCurrency: "CNY", ReservedTokens: 100, ReservedCostMicros: 0, AccountedAt: instant})
				}
				if err != nil {
					t.Fatal(err)
				}
				if !row.CreatedAt.Time.Equal(admitted) || !row.UpdatedAt.Time.Equal(admitted) || !row.CreatedAt.Time.After(oldClock) {
					t.Fatal("reservation silently used the old transaction clock", row.CreatedAt, row.UpdatedAt, oldClock)
				}
				config := store.AiProviderConfigs{DailyTokenLimit: 100}
				if boundary == "month" {
					config = store.AiProviderConfigs{MonthlyTokenLimit: 100}
				}
				if err = checkAIProviderBudget(t.Context(), q, config, principal.TenantID, 1, 0, admitted.Add(time.Second)); !errors.Is(err, ErrAIBudgetExceeded) {
					t.Fatal("new-window budget omitted admitted reservation", err)
				}
			})
		}
	}
}

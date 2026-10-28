package handlers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/ai"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

type providerBudgetModel struct {
	calls     atomic.Int64
	operation string
	delay     time.Duration
}

func (*providerBudgetModel) Name() string { return "explicit-budget-stand-in" }
func (m *providerBudgetModel) Complete(ctx context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
	m.calls.Add(1)
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return ai.CompletionResponse{}, ctx.Err()
		}
	}
	content := `{"category":"work","score":0.8}`
	if m.operation == "draft_reply" {
		content = `{"subject":"Re: synthetic fixture","body":"A suggestion only."}`
	}
	return ai.CompletionResponse{Content: content, UsageKnown: false}, nil
}
func providerBudgetFixture(t *testing.T, mode, operation string) (*db.Pool, store.PlatformConsumeMailboxTicketRow, *providerBudgetModel, *MailboxAIService, string, ai.OperationInput) {
	t.Helper()
	pool, principal := mailboxAIFixture(t, 30)
	if _, err := pool.Exec(t.Context(), `UPDATE plans SET features=jsonb_set(features,'{ai,concurrency}','8') WHERE code='free';UPDATE subscriptions SET plan_snapshot=jsonb_set(plan_snapshot,'{features,ai,concurrency}','8')`); err != nil {
		t.Fatal(err)
	}
	if mode == "byo" {
		if _, err := pool.Exec(t.Context(), "UPDATE ai_provider_configs SET mode='byo' WHERE tenant_id=$1", principal.TenantID); err != nil {
			t.Fatal(err)
		}
		approveMailboxAICloud(t, pool, principal.TenantID, true)
	}
	model := &providerBudgetModel{operation: operation}
	service := &MailboxAIService{Pool: pool, Provider: func(store.AiProviderConfigs) (ai.Provider, error) { return model, nil }}
	target := mailboxAIVersion(t, pool, principal.TenantID)
	input := ai.OperationInput{Operation: operation, Body: "Synthetic owned mailbox content for a budget boundary."}
	return pool, principal, model, service, target, input
}
func providerBudgetFirst(t *testing.T, pool *db.Pool, principal store.PlatformConsumeMailboxTicketRow, service *MailboxAIService, target string, input ai.OperationInput, mode string) store.PlatformAIUsageRow {
	t.Helper()
	_, err := service.Run(t.Context(), principal, "provider-budget-first-key", input, target)
	if mode == "platform" && !errors.Is(err, errAIBudget) || mode == "byo" && err != nil {
		t.Fatal("unexpected unknown-usage result", mode, err)
	}
	rows, e := store.New(pool).PlatformAIUsage(t.Context(), principal.TenantID)
	if e != nil || len(rows) != 1 {
		t.Fatal("first reservation missing", len(rows), e)
	}
	row := rows[0]
	if row.ActualTokens.Valid || row.ReservedTokens <= 0 {
		t.Fatal("unknown usage lost conservative reservation")
	}
	return row
}
func TestNativeAIProviderBudgetAllWindowsAndModesPG(t *testing.T) {
	for _, test := range []struct {
		mode, operation, column string
		cost                    bool
	}{
		{"platform", "classify", "daily_token_limit", false}, {"platform", "draft_reply", "monthly_token_limit", false},
		{"platform", "classify", "daily_cost_micros", true}, {"platform", "draft_reply", "monthly_cost_micros", true},
		{"byo", "classify", "daily_token_limit", false}, {"byo", "draft_reply", "monthly_token_limit", false},
	} {
		t.Run(test.mode+"_"+test.column, func(t *testing.T) {
			pool, principal, model, service, target, input := providerBudgetFixture(t, test.mode, test.operation)
			first := providerBudgetFirst(t, pool, principal, service, target, input, test.mode)
			unit := first.ReservedTokens
			if test.cost {
				unit = first.ReservedCostMicros
			}
			if unit <= 0 {
				t.Fatal("missing reservation")
			}
			// The column is one of the six fixed literals above, never request input.
			if _, err := pool.Exec(t.Context(), fmt.Sprintf("UPDATE ai_provider_configs SET %s=$1 WHERE tenant_id=$2", test.column), 2*unit, principal.TenantID); err != nil {
				t.Fatal(err)
			}
			_, err := service.Run(t.Context(), principal, "provider-budget-exact-limit", input, target)
			if test.mode == "platform" && !errors.Is(err, errAIBudget) || test.mode == "byo" && err != nil {
				t.Fatal("exact boundary refused", err)
			}
			if _, err = service.Run(t.Context(), principal, "provider-budget-over-limit", input, target); !errors.Is(err, errAIBudget) {
				t.Fatal("crossing provider budget allowed", err)
			}
			rows, e := store.New(pool).PlatformAIUsage(t.Context(), principal.TenantID)
			if e != nil || len(rows) != 2 || model.calls.Load() != 2 {
				t.Fatal("over-budget operation reached provider or wrote intent", len(rows), model.calls.Load(), e)
			}
			_, err = service.Run(t.Context(), principal, "provider-budget-first-key", input, target)
			if test.mode == "platform" && !errors.Is(err, errAIIntent) || test.mode == "byo" && err != nil {
				t.Fatal("existing idempotency contract changed", err)
			}
			if model.calls.Load() != 2 {
				t.Fatal("replay called provider")
			}
		})
	}
}
func TestNativeAIProviderBudgetSerializesReservationsBeforeExternalRPCPG(t *testing.T) {
	for _, mode := range []string{"platform", "byo"} {
		t.Run(mode, func(t *testing.T) {
			pool, principal, model, service, target, input := providerBudgetFixture(t, mode, "classify")
			first := providerBudgetFirst(t, pool, principal, service, target, input, mode)
			if _, err := pool.Exec(t.Context(), "UPDATE ai_provider_configs SET daily_token_limit=$1 WHERE tenant_id=$2", 2*first.ReservedTokens, principal.TenantID); err != nil {
				t.Fatal(err)
			}
			model.delay = 60 * time.Millisecond
			var group sync.WaitGroup
			for n := range 8 {
				group.Add(1)
				go func(index int) {
					defer group.Done()
					_, _ = service.Run(t.Context(), principal, fmt.Sprintf("provider-budget-race-%03d", index), input, target)
				}(n)
			}
			group.Wait()
			rows, err := store.New(pool).PlatformAIUsage(t.Context(), principal.TenantID)
			if err != nil || len(rows) != 2 || model.calls.Load() != 2 {
				t.Fatal("parallel provider budget overrun", len(rows), model.calls.Load(), err)
			}
		})
	}
}
func TestNativeAndSearchAskShareProviderBudgetLedgerPG(t *testing.T) {
	pool, principal, model, service, target, input := providerBudgetFixture(t, "platform", "classify")
	first := providerBudgetFirst(t, pool, principal, service, target, input, "platform")
	question := "Synthetic bounded question"
	total := first.ReservedTokens + int64(len(question)) + 16384
	if _, err := pool.Exec(t.Context(), "UPDATE ai_provider_configs SET monthly_token_limit=$1 WHERE tenant_id=$2", total, principal.TenantID); err != nil {
		t.Fatal(err)
	}
	reservation, err := (AISearchAskAccounting{Pool: pool}).Reserve(t.Context(), principal.TenantID, principal.MailboxID, question)
	if err != nil || reservation == nil {
		t.Fatal("search ask did not share exact boundary", err)
	}
	if _, err = service.Run(t.Context(), principal, "provider-budget-after-search", input, target); !errors.Is(err, errAIBudget) {
		t.Fatal("native ignored grounded answer reservation", err)
	}
	if _, err = (AISearchAskAccounting{Pool: pool}).Reserve(t.Context(), principal.TenantID, principal.MailboxID, question); !errors.Is(err, ErrAIBudgetExceeded) {
		t.Fatal("grounded answer ignored shared reservation", err)
	}
	if model.calls.Load() != 1 {
		t.Fatal("blocked native request reached provider")
	}
}

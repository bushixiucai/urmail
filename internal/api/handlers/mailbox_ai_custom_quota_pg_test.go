package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/ai"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func customQuotaTarget(t *testing.T, pool *db.Pool, principal store.PlatformConsumeMailboxTicketRow, provider string) *userAICustomConfig {
	t.Helper()
	endpoint := "http://127.0.0.1:11434"
	if provider != "ollama" {
		endpoint = "https://byok.example.test/v1"
	}
	prefs, _ := json.Marshal(map[string]any{"ai": map[string]any{"use_custom": true, "provider": provider, "endpoint": endpoint, "model": "custom-fixture"}})
	_, err := pool.Exec(t.Context(), `INSERT INTO mailbox_settings(tenant_id,mailbox_id,preferences) VALUES($1,$2,$3) ON CONFLICT(mailbox_id) DO UPDATE SET preferences=EXCLUDED.preferences,version=mailbox_settings.version+1`, principal.TenantID, principal.MailboxID, prefs)
	if err != nil {
		t.Fatal(err)
	}
	h := &NativeMailboxAI{Service: &MailboxAIService{Pool: pool}}
	target, custom, err := h.getUserAI(t.Context(), webmail.Session{TenantID: principal.TenantID, MailboxID: principal.MailboxID})
	if err != nil || !custom {
		t.Fatalf("custom target: %v", err)
	}
	return target
}

func TestMailboxCustomAIUsesCommonQuotaAndZeroPlatformCostPG(t *testing.T) {
	for _, provider := range []string{"ollama", "openai"} {
		t.Run(provider, func(t *testing.T) {
			pool, principal := mailboxAIFixture(t, 2)
			target := customQuotaTarget(t, pool, principal, provider)
			model := &countedAI{}
			service := &MailboxAIService{Pool: pool, Provider: func(cfg store.AiProviderConfigs) (ai.Provider, error) {
				if cfg.Model != "custom-fixture" || cfg.Mode == "platform" || cfg.InputMicrosPerMillion != 0 || cfg.OutputMicrosPerMillion != 0 || cfg.EncryptedKey != "" {
					t.Error("custom provider bypassed governed projection")
				}
				return model, nil
			}}
			input := ai.OperationInput{Operation: "summarize", Body: "Private user input stays out of accounting metadata"}
			for i, key := range []string{"custom-quota-first-request", "custom-quota-first-request", "custom-quota-second-request"} {
				if _, err := service.RunCustom(t.Context(), principal, key, input, target, target.Digest); err != nil {
					t.Fatal(err)
				}
				if want := []int64{1, 1, 2}[i]; model.calls.Load() != want {
					t.Fatal("idempotency repeated provider", model.calls.Load())
				}
			}
			if _, err := service.RunCustom(t.Context(), principal, "custom-quota-denied-request", input, target, target.Digest); !errors.Is(err, errAIBudget) {
				t.Fatal("custom model bypassed call quota", err)
			}
			rows, err := store.New(pool).PlatformAIUsage(t.Context(), principal.TenantID)
			if err != nil || len(rows) != 2 {
				t.Fatalf("ledger rows=%d err=%v", len(rows), err)
			}
			for _, row := range rows {
				if row.Mode == "platform" || row.ReservedCostMicros != 0 || !row.ActualCostMicros.Valid || row.ActualCostMicros.Int64 != 0 || row.Status != "completed" {
					t.Fatalf("custom platform charge: mode=%s reserved=%d actual=%v status=%s", row.Mode, row.ReservedCostMicros, row.ActualCostMicros, row.Status)
				}
			}
			if model.calls.Load() != 2 {
				t.Fatal("over-quota request called provider")
			}
		})
	}
}

type blockedCustomQuotaModel struct {
	entered, release chan struct{}
	calls            atomic.Int32
}

func (*blockedCustomQuotaModel) Name() string { return "custom-quota-blocker" }
func (m *blockedCustomQuotaModel) Complete(ctx context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
	m.calls.Add(1)
	close(m.entered)
	select {
	case <-ctx.Done():
		return ai.CompletionResponse{}, ctx.Err()
	case <-m.release:
		return ai.CompletionResponse{Content: `{"summary":"done"}`, UsageKnown: true, PromptTokens: 10, OutputTokens: 5}, nil
	}
}

func TestMailboxCustomAIConcurrencyAndSettingsSnapshotPG(t *testing.T) {
	pool, principal := mailboxAIFixture(t, 3)
	target := customQuotaTarget(t, pool, principal, "ollama")
	model := &blockedCustomQuotaModel{entered: make(chan struct{}), release: make(chan struct{})}
	service := &MailboxAIService{Pool: pool, Provider: func(store.AiProviderConfigs) (ai.Provider, error) { return model, nil }}
	input := ai.OperationInput{Operation: "summarize", Body: "Shared concurrency slot"}
	first := make(chan error, 1)
	go func() {
		_, err := service.RunCustom(t.Context(), principal, "custom-concurrency-first", input, target, target.Digest)
		first <- err
	}()
	select {
	case <-model.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	if _, err := service.RunCustom(t.Context(), principal, "custom-concurrency-second", input, target, target.Digest); !errors.Is(err, errAIBudget) {
		close(model.release)
		t.Fatal("custom model bypassed concurrency", err)
	}
	close(model.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "UPDATE mailbox_settings SET version=version+1 WHERE tenant_id=$1 AND mailbox_id=$2", principal.TenantID, principal.MailboxID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunCustom(t.Context(), principal, "custom-concurrency-first", input, target, target.Digest); !errors.Is(err, errAITargetChanged) {
		t.Fatal("stale config snapshot returned cached work", err)
	}
	if model.calls.Load() != 1 {
		t.Fatal("denied request reached custom model")
	}
}

func TestMailboxCustomAskReservationSharesBudgetWithoutPlatformChargePG(t *testing.T) {
	pool, principal := mailboxAIFixture(t, 1)
	target := customQuotaTarget(t, pool, principal, "ollama")
	account := AISearchAskAccounting{Pool: pool}
	reservation, err := account.ReserveCustom(t.Context(), principal.TenantID, principal.MailboxID, "Confirmed source question", target)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.New(pool).PlatformAIUsage(t.Context(), principal.TenantID)
	if err != nil || len(rows) != 1 || rows[0].Mode != "local" || rows[0].ReservedCostMicros != 0 || reservation.mode == "platform" {
		t.Fatalf("custom ask charged platform: rows=%v err=%v", rows, err)
	}
	if _, err = account.ReserveCustom(t.Context(), principal.TenantID, principal.MailboxID, "second", target); !errors.Is(err, ErrAIBudgetExceeded) {
		t.Fatal("custom ask bypassed quota", err)
	}
	// A disabled tenant provider must not turn into ErrNotAccounted, which the
	// caller intentionally tolerates only on its legacy unconfigured path.
	if _, err = pool.Exec(t.Context(), "UPDATE ai_provider_configs SET enabled=false WHERE tenant_id=$1", principal.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = account.ReserveCustom(t.Context(), principal.TenantID, principal.MailboxID, "disabled", target); err == nil || errors.Is(err, ErrNotAccounted) {
		t.Fatal("disabled custom provider skipped accounting", err)
	}
	reservation.releaseUnused()
	deadline := time.Now().Add(3 * time.Second)
	for {
		current, readErr := store.New(pool).PlatformAIUsage(t.Context(), principal.TenantID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(current) == 1 && current[0].Status == "released" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("unused custom reservation did not settle")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMailboxCustomAITokenBudgetsAndLocalOnlyRemainAuthoritativePG(t *testing.T) {
	pool, principal := mailboxAIFixture(t, 3)
	target := customQuotaTarget(t, pool, principal, "openai")
	model := &countedAI{}
	service := &MailboxAIService{Pool: pool, Provider: func(store.AiProviderConfigs) (ai.Provider, error) { return model, nil }}
	input := ai.OperationInput{Operation: "summarize", Body: "budget guarded"}
	if _, err := pool.Exec(t.Context(), "UPDATE ai_provider_configs SET daily_token_limit=1 WHERE tenant_id=$1", principal.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunCustom(t.Context(), principal, "custom-token-budget-denied", input, target, target.Digest); !errors.Is(err, errAIBudget) {
		t.Fatal("custom token budget bypassed", err)
	}
	if _, err := (AISearchAskAccounting{Pool: pool}).ReserveCustom(t.Context(), principal.TenantID, principal.MailboxID, "question", target); !errors.Is(err, ErrAIBudgetExceeded) {
		t.Fatal("custom ask token budget bypassed", err)
	}
	if _, err := pool.Exec(t.Context(), "UPDATE ai_provider_configs SET daily_token_limit=0,local_only=true WHERE tenant_id=$1", principal.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunCustom(t.Context(), principal, "custom-local-policy-denied", input, target, target.Digest); !errors.Is(err, ai.ErrLocalOnly) {
		t.Fatal("custom cloud bypassed local-only governance", err)
	}
	if model.calls.Load() != 0 {
		t.Fatal("denied custom request called provider")
	}
}

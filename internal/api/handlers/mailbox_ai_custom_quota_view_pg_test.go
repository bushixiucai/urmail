package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/store"
)

func TestMailboxCustomQuotaViewMatchesEffectiveTargetWithoutDecryptingPG(t *testing.T) {
	f := newNativeAIHTTPFixture(t)
	token := loginNative(t, f.router, f.principal.Address, f.password)
	q := store.New(f.pool)
	if err := q.PlatformSetNonplatformAIQuota(t.Context(), store.PlatformSetNonplatformAIQuotaParams{TenantID: f.principal.TenantID, NonplatformCallLimit: 7, NonplatformMailboxCallLimit: 3, NonplatformConcurrencyLimit: 2, QuotaTimezone: "Asia/Shanghai"}); err != nil {
		t.Fatal(err)
	}
	if err := q.PlatformSetAIBudget(t.Context(), store.PlatformSetAIBudgetParams{TenantID: f.principal.TenantID, DailyTokenLimit: 90000, MonthlyTokenLimit: 180000, DailyCostMicros: 100, MonthlyCostMicros: 200}); err != nil {
		t.Fatal(err)
	}
	baseline, err := q.PlatformGetAIProvider(t.Context(), f.principal.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"openai", "ollama"} {
		t.Run(provider, func(t *testing.T) {
			endpoint := "https://byok.example.test/v1"
			if provider == "ollama" {
				endpoint = "http://127.0.0.1:11434"
			}
			// No SecretsBox exists. A metadata-only quota projection must not decrypt this
			// opaque ciphertext or construct/contact a provider to report the caps.
			prefs, _ := json.Marshal(map[string]any{"ai": map[string]any{"use_custom": true, "provider": provider, "model": "own-model", "endpoint": endpoint, "api_key": "enc:v1:opaque-metadata-fixture"}})
			if _, err = f.pool.Exec(t.Context(), `INSERT INTO mailbox_settings(tenant_id,mailbox_id,preferences) VALUES($1,$2,$3) ON CONFLICT(mailbox_id) DO UPDATE SET preferences=EXCLUDED.preferences,version=mailbox_settings.version+1`, f.principal.TenantID, f.principal.MailboxID, prefs); err != nil {
				t.Fatal(err)
			}
			response := settingsRequest(t, f.router, token, http.MethodGet, "/api/v1/mailbox/ai/budget", nil)
			var view struct {
				Enabled     bool   `json:"enabled"`
				Mode        string `json:"mode"`
				Entitlement struct {
					Source       string `json:"source"`
					Calls        int64  `json:"call_limit"`
					MailboxCalls int64  `json:"mailbox_call_limit"`
					Concurrency  int64  `json:"concurrency_limit"`
					Timezone     string `json:"timezone"`
				} `json:"entitlement"`
				Daily struct {
					Tokens    int64 `json:"tokens_limit"`
					CostUsed  int64 `json:"cost_micros_used"`
					CostLimit int64 `json:"cost_micros_limit"`
				} `json:"daily"`
			}
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &view) != nil {
				t.Fatalf("quota view %d %s", response.Code, response.Body.String())
			}
			mode := "byo"
			if provider == "ollama" {
				mode = "local"
			}
			if !view.Enabled || view.Mode != mode || view.Entitlement.Source != "configured_nonplatform" || view.Entitlement.Calls != 7 || view.Entitlement.MailboxCalls != 3 || view.Entitlement.Concurrency != 2 || view.Entitlement.Timezone != "Asia/Shanghai" || view.Daily.Tokens != 90000 || view.Daily.CostUsed != 0 || view.Daily.CostLimit != 0 {
				t.Fatalf("quota view differs from governed custom mode: %s", response.Body.String())
			}
			if strings.Contains(response.Body.String(), "enc:v1:") || strings.Contains(response.Body.String(), "byok.example.test") {
				t.Fatal("quota view exposed provider credentials or configuration")
			}
		})
	}
	stored, err := q.PlatformGetAIProvider(t.Context(), f.principal.TenantID)
	if err != nil || stored.Mode != baseline.Mode || stored.Provider != baseline.Provider || stored.Model != baseline.Model || stored.InputMicrosPerMillion != baseline.InputMicrosPerMillion {
		t.Fatal("quota read mutated tenant governance", err)
	}
	if f.model.calls.Load() != 0 {
		t.Fatal("quota read called a provider")
	}
}

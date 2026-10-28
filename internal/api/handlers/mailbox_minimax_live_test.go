package handlers

// This opt-in test sends only the synthetic invoice authored in the fixture.
// No customer mailbox, external recipient or production consent row is used.
import (
	"crypto/rand"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func TestNativeMailboxMiniMaxLiveSynthetic(t *testing.T) {
	path := os.Getenv("URMAIL_MINIMAX_CONFIG_FILE")
	if path == "" || testing.Short() {
		t.Skip("explicit private MiniMax configuration required")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatal("private provider configuration must be readable only by owner")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("private provider configuration unavailable")
	}
	var cfg struct {
		Endpoint string `json:"endpoint"`
		Key      string `json:"api_key"`
		Model    string `json:"model"`
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.Key == "" || cfg.Endpoint != "https://api.minimax.cn/v1" || cfg.Model != "MiniMax-M3.1-Flash" {
		t.Fatal("unexpected provider target")
	}
	f := newNativeAIHTTPFixture(t)
	token := loginNative(t, f.router, f.principal.Address, f.password)
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		t.Fatal("ephemeral encryption key unavailable")
	}
	defer clear(key)
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal("ephemeral secret box unavailable")
	}
	encrypted, err := box.EncryptString(cfg.Key)
	if err != nil {
		t.Fatal("provider encryption failed")
	}
	q := store.New(f.pool)
	if _, err = q.PlatformSetAIProvider(t.Context(), store.PlatformSetAIProviderParams{TenantID: f.principal.TenantID, Mode: "byo", Provider: "openai", Endpoint: cfg.Endpoint, Model: cfg.Model, EncryptedKey: encrypted, Enabled: true, CostCurrency: "CNY"}); err != nil {
		t.Fatal(err)
	}
	approveMailboxAICloud(t, f.pool, f.principal.TenantID, true)
	f.aiService.Provider = nil
	f.aiService.Secrets = box
	f.aiService.CloudEndpoints = []string{cfg.Endpoint}
	f.search.ModelFor = TenantCitationModel(f.pool, box, []string{cfg.Endpoint}, nil)
	target, err := q.PlatformGetAIProvider(t.Context(), f.principal.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	operations := []string{"classify", "summarize", "translate", "phishing", "polish", "draft_new", "draft_reply"}
	if os.Getenv("URMAIL_MINIMAX_QA_ONLY") == "true" {
		operations = nil
	}
	for _, operation := range operations {
		input := map[string]any{"operation": operation, "configuration_digest": target.ConfigurationDigest, "confirmed": true, "request_key": "minimax-live-synthetic-" + operation, "instruction": "Write or analyze only this synthetic invoice. Be concise.", "body": "Please confirm receipt of our synthetic 42 euro invoice. No payment should actually be made.", "target_language": "zh-CN"}
		if operation != "draft_new" {
			input["folder_enc"] = webmail.EncodeFolderName("INBOX")
			input["uid"] = 5
			input["uid_validity"] = 9
			input["selection"] = "The October invoice total is 42 euros. Payment is due on Friday."
		}
		response := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input)
		if response.Code != 200 {
			var failure struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			_ = json.Unmarshal(response.Body.Bytes(), &failure)
			t.Fatalf("operation=%s HTTP=%d code=%s", operation, response.Code, failure.Error.Code)
		}
		t.Logf("native %s: HTTP 200, strict suggestion schema accepted", operation)
	}
	answer := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/search-ask", map[string]any{"question": "What is the invoice total?", "configuration_digest": target.ConfigurationDigest, "confirmed": true, "folder_enc": webmail.EncodeFolderName("INBOX")})
	var result struct {
		Grounded  bool     `json:"grounded"`
		Citations []string `json:"citations"`
	}
	if answer.Code != 200 || json.Unmarshal(answer.Body.Bytes(), &result) != nil || !result.Grounded || len(result.Citations) == 0 {
		t.Fatalf("native sourced question failed HTTP=%d", answer.Code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := q.PlatformAIUsage(t.Context(), f.principal.TenantID)
		if err == nil && len(rows) > 0 && rows[0].Status != "reserved" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	rows, err := q.PlatformAIUsage(t.Context(), f.principal.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	var actual int64
	for _, row := range rows {
		if row.ActualTokens.Valid {
			actual += row.ActualTokens.Int64
		}
	}
	if actual <= 0 {
		t.Fatal("real provider usage missing from native ledger")
	}
	t.Logf("native sourced question HTTP 200, authorized citations=%d; observed actual ledger tokens=%d", len(result.Citations), actual)
}

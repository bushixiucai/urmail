package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/ai"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// TestMailboxAIDegradesToPlainTextAfterSchemaRetry pins TECH §3 at the
// HTTP boundary: when the structured answer fails schema validation twice
// (initial call plus the corrective retry), the pipeline resends the
// operation as plain text and serves it behind result.text with
// degraded=true and degrade_reason=schema_retry_exhausted. The user gets
// an answer instead of an error, and the response says it is degraded.
func TestMailboxAIDegradesToPlainTextAfterSchemaRetry(t *testing.T) {
	f := newNativeAIHTTPFixture(t)
	token := loginNative(t, f.router, f.principal.Address, f.password)
	cfg, err := store.New(f.pool).PlatformGetAIProvider(t.Context(), f.principal.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	f.model.respond = func(request ai.CompletionRequest) (string, bool) {
		if !request.JSONMode {
			// The plain-text fallback resend.
			return "Invoice total: 42 euros.", true
		}
		// Structured attempts answer with something the schema rejects.
		return "not a json object", true
	}
	input := map[string]any{
		"operation":            "summarize",
		"configuration_digest": cfg.ConfigurationDigest,
		"confirmed":            true,
		"request_key":          "native-ai-degrade-000001",
		"folder_enc":           webmail.EncodeFolderName("INBOX"),
		"uid":                  5,
		"uid_validity":         9,
		"selection":            "The October invoice total is 42 euros.",
	}
	response := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input)
	if response.Code != 200 {
		t.Fatalf("assist status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Result struct {
			Text string `json:"text"`
		} `json:"result"`
		Degraded        bool   `json:"degraded"`
		DegradeReason   string `json:"degrade_reason"`
		TemplateVersion int    `json:"template_version"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid envelope: %s", response.Body.String())
	}
	if !envelope.Degraded || envelope.DegradeReason != "schema_retry_exhausted" {
		t.Fatalf("expected degraded envelope: %s", response.Body.String())
	}
	if envelope.Result.Text != "Invoice total: 42 euros." {
		t.Fatalf("degraded text missing: %s", response.Body.String())
	}
	// One structured attempt, one corrective retry and one plain-text
	// fallback - all inside one billed operation.
	if f.model.calls.Load() != 3 {
		t.Fatalf("provider calls=%d", f.model.calls.Load())
	}
	if !strings.Contains(response.Body.String(), "degraded") {
		t.Fatalf("degraded flag missing: %s", response.Body.String())
	}
}

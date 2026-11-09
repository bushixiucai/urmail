package handlers

import (
	"encoding/json"
	"net/http/httptest"
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

// TestMailboxAIDegradesWhenStructuredCallFails pins the DEF-B fix at the
// HTTP boundary: translate whose structured JSON call fails on the
// provider side (v0.6.12 QA saw two 502 AI_UNAVAILABLE at ~30s) must not
// surface a hard error — the pipeline degrades through the plain-text
// fallback resend and the user gets a labeled usable answer. Degraded
// answers are never cached, so a repeat pays the same degrade path.
func TestMailboxAIDegradesWhenStructuredCallFails(t *testing.T) {
	f := newNativeAIHTTPFixture(t)
	token := loginNative(t, f.router, f.principal.Address, f.password)
	cfg, err := store.New(f.pool).PlatformGetAIProvider(t.Context(), f.principal.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	f.model.fail = func(request ai.CompletionRequest) bool { return request.JSONMode }
	f.model.respond = func(request ai.CompletionRequest) (string, bool) {
		if !request.JSONMode {
			return "发票总金额为42欧元，请于星期五之前付款。", true
		}
		return "", false
	}
	input := func(requestKey string) map[string]any {
		return map[string]any{
			"operation":            "translate",
			"configuration_digest": cfg.ConfigurationDigest,
			"confirmed":            true,
			"request_key":          requestKey,
			"folder_enc":           webmail.EncodeFolderName("INBOX"),
			"uid":                  5,
			"uid_validity":         9,
			// Selection must be a literal substring of the fixture message body
			// ("The October invoice total is 42 euros. Payment is due on Friday.
			// Please confirm the bank details independently.") or the Assist
			// selection guard rejects with 409 AI_SOURCE_CHANGED before the
			// pipeline runs.
			"selection":       "The October invoice total is 42 euros.",
			"target_language": "zh-CN",
		}
	}
	decode := func(w *httptest.ResponseRecorder, requestKey string) {
		t.Helper()
		if w.Code != 200 {
			t.Fatalf("%s: status=%d body=%s", requestKey, w.Code, w.Body.String())
		}
		var envelope struct {
			Result struct {
				Text string `json:"text"`
			} `json:"result"`
			Degraded      bool   `json:"degraded"`
			DegradeReason string `json:"degrade_reason"`
			CacheHit      bool   `json:"cache_hit"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("%s: invalid envelope: %s", requestKey, w.Body.String())
		}
		if !envelope.Degraded || envelope.DegradeReason != "provider_unavailable" {
			t.Fatalf("%s: expected degraded/provider_unavailable envelope: %s", requestKey, w.Body.String())
		}
		if envelope.CacheHit {
			t.Fatalf("%s: degraded answer must not be cached", requestKey)
		}
		if !strings.Contains(envelope.Result.Text, "42欧元") {
			t.Fatalf("%s: degraded text %q", requestKey, envelope.Result.Text)
		}
	}

	first := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input("defb-degrade-000001"))
	decode(first, "first")
	if got := f.model.calls.Load(); got != 2 {
		t.Fatalf("first request provider calls=%d, want 2 (structured fail + fallback resend)", got)
	}

	second := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input("defb-degrade-000002"))
	decode(second, "second")
	if got := f.model.calls.Load(); got != 4 {
		t.Fatalf("second request provider calls=%d, want 4 (degraded answers stay uncached)", got)
	}
}

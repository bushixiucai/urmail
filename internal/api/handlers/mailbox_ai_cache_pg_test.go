package handlers

import (
	"encoding/json"
	"testing"

	"github.com/bushixiucai/urmail/internal/ai"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// TestMailboxAIServesRepeatFromResultCache pins TECH §4 at the HTTP
// boundary: the second identical summarize of the same mail is served
// from the result cache — the provider is not called again, the response
// carries cache_hit=true with the same result, and a different request
// key proves the cache (not reservation idempotency) did the dedupe.
func TestMailboxAIServesRepeatFromResultCache(t *testing.T) {
	f := newNativeAIHTTPFixture(t)
	f.aiService.ResultCache = ai.NewResultCache()
	token := loginNative(t, f.router, f.principal.Address, f.password)
	cfg, err := store.New(f.pool).PlatformGetAIProvider(t.Context(), f.principal.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	input := func(requestKey string) map[string]any {
		return map[string]any{
			"operation":            "summarize",
			"configuration_digest": cfg.ConfigurationDigest,
			"confirmed":            true,
			"request_key":          requestKey,
			"folder_enc":           webmail.EncodeFolderName("INBOX"),
			"uid":                  5,
			"uid_validity":         9,
			"selection":            "The October invoice total is 42 euros.",
		}
	}
	first := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input("native-ai-cache-000001"))
	if first.Code != 200 {
		t.Fatalf("first assist status=%d body=%s", first.Code, first.Body.String())
	}
	var envelope struct {
		CacheHit        bool            `json:"cache_hit"`
		TemplateVersion int             `json:"template_version"`
		Result          json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid envelope: %s", first.Body.String())
	}
	if envelope.CacheHit {
		t.Fatalf("first response claims cache hit: %s", first.Body.String())
	}
	if f.model.calls.Load() != 1 {
		t.Fatalf("provider calls after first=%d", f.model.calls.Load())
	}

	second := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input("native-ai-cache-000002"))
	if second.Code != 200 {
		t.Fatalf("second assist status=%d body=%s", second.Code, second.Body.String())
	}
	var secondEnvelope struct {
		CacheHit        bool            `json:"cache_hit"`
		TemplateVersion int             `json:"template_version"`
		Result          json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondEnvelope); err != nil {
		t.Fatalf("invalid envelope: %s", second.Body.String())
	}
	if !secondEnvelope.CacheHit {
		t.Fatalf("second response not served from cache: %s", second.Body.String())
	}
	if f.model.calls.Load() != 1 {
		t.Fatalf("provider called again on cache hit: %d", f.model.calls.Load())
	}
	if string(envelope.Result) != string(secondEnvelope.Result) {
		t.Fatalf("cached result differs:\nfirst %s\nsecond %s", envelope.Result, secondEnvelope.Result)
	}

	// A different selection is a different digest: no cache reuse.
	changed := input("native-ai-cache-000003")
	changed["selection"] = "Payment is due on Friday."
	third := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", changed)
	if third.Code != 200 {
		t.Fatalf("third assist status=%d body=%s", third.Code, third.Body.String())
	}
	if f.model.calls.Load() != 2 {
		t.Fatalf("changed selection did not reach the provider: %d", f.model.calls.Load())
	}
}

package handlers

import (
	"encoding/json"
	"net/http/httptest"
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

// TestMailboxAIResultCacheKeyIncludesLengthLimit pins the DEF-A fix at
// the HTTP boundary: the summarize "shorter" chip sends length_limit and
// a capped call must never be served a default-length cached answer. The
// cache key rides the digest folded over the whole OperationInput, so a
// different cap means a different provider call; a repeated cap is a hit.
func TestMailboxAIResultCacheKeyIncludesLengthLimit(t *testing.T) {
	f := newNativeAIHTTPFixture(t)
	f.aiService.ResultCache = ai.NewResultCache()
	token := loginNative(t, f.router, f.principal.Address, f.password)
	cfg, err := store.New(f.pool).PlatformGetAIProvider(t.Context(), f.principal.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	input := func(requestKey string, lengthLimit int) map[string]any {
		body := map[string]any{
			"operation":            "summarize",
			"configuration_digest": cfg.ConfigurationDigest,
			"confirmed":            true,
			"request_key":          requestKey,
			"folder_enc":           webmail.EncodeFolderName("INBOX"),
			"uid":                  5,
			"uid_validity":         9,
			"selection":            "The October invoice total is 42 euros.",
		}
		if lengthLimit > 0 {
			body["length_limit"] = lengthLimit
		}
		return body
	}
	decode := func(w *httptest.ResponseRecorder) bool {
		t.Helper()
		var envelope struct {
			CacheHit bool `json:"cache_hit"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("invalid envelope: %s", w.Body.String())
		}
		return envelope.CacheHit
	}

	// Default-length call renders and caches under the default key.
	first := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input("defa-len-0000001", 0))
	if first.Code != 200 {
		t.Fatalf("default call status=%d body=%s", first.Code, first.Body.String())
	}
	if decode(first) {
		t.Fatalf("first call claims cache hit: %s", first.Body.String())
	}

	// The QA repro: same mail, length_limit=100. Before the fix this was
	// served from the default-length cache entry (QA saw 5 unchanged
	// bullets with cache_hit=true); now it must reach the provider.
	capped := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input("defa-len-0000002", 100))
	if capped.Code != 200 {
		t.Fatalf("capped call status=%d body=%s", capped.Code, capped.Body.String())
	}
	if decode(capped) {
		t.Fatalf("capped call was served the default-length cached answer")
	}
	if f.model.calls.Load() != 2 {
		t.Fatalf("provider calls after capped=%d, want 2 (length_limit must change the key)", f.model.calls.Load())
	}

	// A repeat of the capped call is a cache hit on its own key.
	cappedAgain := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input("defa-len-0000003", 100))
	if cappedAgain.Code != 200 {
		t.Fatalf("repeat capped call status=%d", cappedAgain.Code)
	}
	if !decode(cappedAgain) {
		t.Fatalf("repeat capped call did not hit the capped cache entry")
	}
	if f.model.calls.Load() != 2 {
		t.Fatalf("provider called again on capped cache hit: %d", f.model.calls.Load())
	}

	// Normalized options share one key: no cache splitting for -5 vs 0.
	for _, probe := range []struct {
		key   string
		limit int
	}{
		{"defa-len-0000004", -5},
		{"defa-len-0000005", 0},
	} {
		resp := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input(probe.key, probe.limit))
		if resp.Code != 200 {
			t.Fatalf("probe %s status=%d", probe.key, resp.Code)
		}
		if !decode(resp) {
			t.Fatalf("probe %s (limit %d) did not hit the default cache entry", probe.key, probe.limit)
		}
	}
	if f.model.calls.Load() != 2 {
		t.Fatalf("unnormalized options split the cache: %d provider calls", f.model.calls.Load())
	}
}

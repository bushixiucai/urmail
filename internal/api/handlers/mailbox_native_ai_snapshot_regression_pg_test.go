package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/bushixiucai/urmail/internal/domain/aianswer"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type aiSwitchOnValidation struct {
	MailboxNativeSessions
	change func()
}

func (s aiSwitchOnValidation) Get(ctx context.Context, key string) (webmail.Session, error) {
	s.change()
	return s.MailboxNativeSessions.Get(ctx, key)
}

// A confirmation must bind the provider snapshot actually used, not a later
// validation read that happens to match the user's digest.
func TestNativeAIRejectsProviderSnapshotChangedBetweenReadsPG(t *testing.T) {
	f := newNativeAIHTTPFixture(t)
	token := loginNative(t, f.router, f.principal.Address, f.password)
	var exports atomic.Int32
	provider := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			exports.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"model": "fixture-model", "message": map[string]string{"role": "assistant", "content": `{"summary":"fixture"}`}, "done": true, "done_reason": "stop"})
		}))
	}
	a, b := provider(), provider()
	defer a.Close()
	defer b.Close()
	f.aiService.LocalEndpoints = []string{a.URL, b.URL}
	setConfig := func(endpoint string, version int) {
		t.Helper()
		prefs, _ := json.Marshal(map[string]any{"ai": map[string]any{"use_custom": true, "provider": "ollama", "endpoint": endpoint, "model": "fixture-model"}})
		_, err := f.pool.Exec(t.Context(), `INSERT INTO mailbox_settings(tenant_id,mailbox_id,preferences,version) VALUES($1,$2,$3,$4) ON CONFLICT(mailbox_id) DO UPDATE SET preferences=EXCLUDED.preferences,version=EXCLUDED.version`, f.principal.TenantID, f.principal.MailboxID, prefs, version)
		if err != nil {
			t.Fatal(err)
		}
	}
	setConfig(b.URL, 8)
	h := &NativeMailboxAI{Native: f.native, Service: f.aiService}
	config, custom, err := h.getUserAI(t.Context(), webmail.Session{TenantID: f.principal.TenantID, MailboxID: f.principal.MailboxID})
	if err != nil || !custom {
		t.Fatalf("custom target: %v", err)
	}
	input := map[string]any{"operation": "summarize", "configuration_digest": config.Digest, "preview_only": true, "body": "Confirmed only for provider B", "request_key": "snapshot-regression-request-0001"}
	preview := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input)
	var approved struct {
		Digest string `json:"content_digest"`
	}
	if preview.Code != 200 || json.Unmarshal(preview.Body.Bytes(), &approved) != nil || approved.Digest == "" {
		t.Fatalf("preview: %d %s", preview.Code, preview.Body.String())
	}
	setConfig(a.URL, 7)
	// Middleware still uses its original store; this hook runs inside requireTarget,
	// after Assist has already read provider A into its local snapshot.
	f.native.Sessions = aiSwitchOnValidation{MailboxNativeSessions: f.native.Sessions, change: func() { setConfig(b.URL, 8) }}
	input["preview_only"], input["confirmed"], input["content_digest"] = false, true, approved.Digest
	response := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input)
	if response.Code != 409 || exports.Load() != 0 {
		t.Fatalf("unconfirmed provider snapshot exported content: status=%d calls=%d body=%s", response.Code, exports.Load(), response.Body.String())
	}
}

func TestNativeAIPreviewsKeepLedgerAndProviderUntouchedPG(t *testing.T) {
	f := newNativeAIHTTPFixture(t)
	token := loginNative(t, f.router, f.principal.Address, f.password)
	configuration, err := store.New(f.pool).PlatformGetAIProvider(t.Context(), f.principal.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	var askCalls atomic.Int32
	factory := f.search.ModelFor
	f.search.ModelFor = func(ctx context.Context, tenant int64) (aianswer.Model, error) {
		model, err := factory(ctx, tenant)
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context, question string, sources []aianswer.Source) (string, []string, error) {
			askCalls.Add(1)
			return model(ctx, question, sources)
		}, nil
	}
	for _, test := range []struct {
		path  string
		input map[string]any
	}{
		{"/api/v1/mailbox/ai/assist", map[string]any{"operation": "summarize", "body": "Only the previewed content", "request_key": "review-preview-without-charge"}},
		{"/api/v1/mailbox/ai/search-ask", map[string]any{"question": "What is the invoice total?"}},
	} {
		test.input["preview_only"], test.input["confirmed"], test.input["configuration_digest"] = true, false, configuration.ConfigurationDigest
		response := nativeAIJSON(t, f, token, test.path, test.input)
		var preview struct {
			Digest  string            `json:"content_digest"`
			Sources []json.RawMessage `json:"sources"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &preview) != nil || preview.Digest == "" {
			t.Fatalf("preview %s: %d %s", test.path, response.Code, response.Body.String())
		}
		if test.path == "/api/v1/mailbox/ai/search-ask" && len(preview.Sources) == 0 {
			t.Fatal("Q&A preview did not exercise grounded sources")
		}
	}
	rows, err := store.New(f.pool).PlatformAIUsage(t.Context(), f.principal.TenantID)
	if err != nil || len(rows) != 0 || askCalls.Load() != 0 || f.model.calls.Load() != 0 {
		t.Fatalf("preview reserved usage or invoked provider: rows=%d ask=%d assist=%d err=%v", len(rows), askCalls.Load(), f.model.calls.Load(), err)
	}
}

package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestMailboxAISettingsEncryptedRoundTripPG(t *testing.T) {
	f := newNativeAIHTTPFixture(t)
	box, err := secrets.New(bytes.Repeat([]byte{41}, 32))
	if err != nil {
		t.Fatal(err)
	}
	f.native.Secrets, f.aiService.Secrets = box, box
	token := loginNative(t, f.router, f.principal.Address, f.password)
	config := map[string]any{"use_custom": true, "provider": "openai", "endpoint": "https://provider.example.test/v1", "model": "example", "api_key": "write-only-fixture-key"}
	save := func(version int) *httptest.ResponseRecorder {
		return settingsRequest(t, f.router, token, http.MethodPut, "/api/v1/mailbox/settings", map[string]any{"display_name": "Updated sender", "signature_text": "Updated signature", "expected_version": version, "preferences": map[string]any{"timezone": "Asia/Shanghai", "ai": config}})
	}
	if response := save(0); response.Code != 200 {
		t.Fatalf("typed settings rejected: %d %s", response.Code, response.Body.String())
	}
	readStored := func() string {
		row, e := store.New(f.pool).PlatformGetMailboxSettings(t.Context(), store.PlatformGetMailboxSettingsParams{TenantID: f.principal.TenantID, MailboxID: f.principal.MailboxID})
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(string(row.Preferences), "write-only-fixture-key") {
			t.Fatal("plaintext key persisted")
		}
		var prefs struct {
			AI struct {
				Key string `json:"api_key"`
			} `json:"ai"`
		}
		if json.Unmarshal(row.Preferences, &prefs) != nil {
			t.Fatal("invalid preferences")
		}
		return prefs.AI.Key
	}
	sealed := readStored()
	if !strings.HasPrefix(sealed, "enc:v1:") {
		t.Fatal("missing encrypted secret")
	}
	response := settingsRequest(t, f.router, token, http.MethodGet, "/api/v1/mailbox/settings", nil)
	if response.Code != 200 || strings.Contains(response.Body.String(), "enc:v1:") || strings.Contains(response.Body.String(), "write-only-fixture-key") || !strings.Contains(response.Body.String(), `"key_configured":true`) {
		t.Fatal("GET exposed secret or lost key presence")
	}
	config["api_key"] = ""
	if response = save(1); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	kept, err := box.DecryptString(readStored())
	if err != nil || kept != "write-only-fixture-key" {
		t.Fatal("blank input lost secret")
	}
	config["use_custom"], config["clear_api_key"] = false, true
	if response = save(2); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if readStored() != "" {
		t.Fatal("explicit clear retained secret")
	}
}

func TestMailboxBYOKRechecksSessionAfterProviderPG(t *testing.T) {
	f := newNativeAIHTTPFixture(t)
	f.aiService.Provider = nil
	box, err := secrets.New(bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	f.native.Secrets, f.aiService.Secrets = box, box
	token := loginNative(t, f.router, f.principal.Address, f.password)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected provider request %s %s", r.Method, r.URL.Path)
		}
		calls.Add(1)
		if err := f.native.Sessions.Revoke(r.Context(), mailboxprincipal.HashToken(token)); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"summary\":\"Must not escape revoked session\"}"},"finish_reason":"stop"}]}`))
	}))
	defer provider.Close()
	endpoint := provider.URL + "/v1"
	f.native.AILocalEndpoints = []string{endpoint}
	f.aiService.LocalEndpoints = []string{endpoint}
	response := settingsRequest(t, f.router, token, http.MethodPut, "/api/v1/mailbox/settings", map[string]any{"expected_version": 0, "preferences": map[string]any{"ai": map[string]any{"use_custom": true, "provider": "ollama", "endpoint": endpoint, "model": "test"}}})
	if response.Code != 200 {
		t.Fatalf("save custom target: %d %s", response.Code, response.Body.String())
	}
	current := settingsRequest(t, f.router, token, http.MethodGet, "/api/v1/mailbox/ai/target", nil)
	var target struct {
		Digest string `json:"configuration_digest"`
	}
	if json.Unmarshal(current.Body.Bytes(), &target) != nil || target.Digest == "" {
		t.Fatal("target unavailable")
	}
	response = nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", map[string]any{"operation": "summarize", "body": "Selected mail", "configuration_digest": target.Digest, "confirmed": true, "request_key": "byok-session-revocation-0001"})
	if response.Code != 401 || calls.Load() != 1 || strings.Contains(response.Body.String(), "Must not escape") {
		t.Fatalf("revoked custom output served: %d calls=%d", response.Code, calls.Load())
	}
}

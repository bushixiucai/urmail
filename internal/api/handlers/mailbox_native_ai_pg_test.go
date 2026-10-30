package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func nativeAIJSON(t *testing.T, f nativeAIHTTPFixture, token, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	if input, ok := body.(map[string]any); ok && input["confirmed"] == true && input["preview_only"] != true && input["content_digest"] == nil && (path == "/api/v1/mailbox/ai/assist" || path == "/api/v1/mailbox/ai/search-ask") {
		previewInput := make(map[string]any, len(input)+1)
		for key, value := range input {
			previewInput[key] = value
		}
		previewInput["preview_only"] = true
		response := nativeAIJSON(t, f, token, path, previewInput)
		if response.Code != 200 {
			return response
		}
		var preview struct {
			Digest string `json:"content_digest"`
		}
		if json.Unmarshal(response.Body.Bytes(), &preview) != nil || preview.Digest == "" {
			t.Fatal("preview digest missing")
		}
		input["content_digest"] = preview.Digest
		defer delete(input, "content_digest")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}
func TestNativeAIAssistScopePreviewAndRevocationPG(t *testing.T) {
	f := newNativeAIHTTPFixture(t)
	token := loginNative(t, f.router, f.principal.Address, f.password)
	cfg, err := store.New(f.pool).PlatformGetAIProvider(t.Context(), f.principal.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"operation": "summarize", "configuration_digest": cfg.ConfigurationDigest, "confirmed": true, "request_key": "native-ai-read-http-000001", "folder_enc": webmail.EncodeFolderName("INBOX"), "uid": 5, "uid_validity": 9, "selection": "The October invoice total is 42 euros."}
	input["confirmed"] = false
	if response := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input); response.Code != 403 || f.model.calls.Load() != 0 {
		t.Fatal("unconfirmed content exported")
	}
	input["confirmed"] = true
	input["selection"] = ""
	if response := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input); response.Code != 400 || f.model.calls.Load() != 0 {
		t.Fatal("empty excerpt exported whole message")
	}
	input["selection"] = "Not present in the original"
	if response := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input); response.Code != 409 || f.model.calls.Load() != 0 {
		t.Fatal("unselected text accepted as original")
	}
	input["selection"] = "The October invoice total is 42 euros."
	input["uid_validity"] = 10
	if response := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input); response.Code != 409 || f.model.calls.Load() != 0 {
		t.Fatal("UIDVALIDITY drift accepted")
	}
	input["uid_validity"] = 9
	for index, operation := range []string{"summarize", "translate", "classify", "phishing", "polish"} {
		input["operation"] = operation
		input["request_key"] = "native-ai-five-operation-" + operation
		input["target_language"] = "zh-CN"
		response := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input)
		if response.Code != 200 {
			t.Fatalf("operation %s status=%d body=%s", operation, response.Code, response.Body.String())
		}
		if f.model.calls.Load() != int64(index+1) {
			t.Fatal("unexpected provider calls")
		}
	}
	before := f.model.calls.Load()
	approveMailboxAICloud(t, f.pool, f.principal.TenantID, false)
	input["request_key"] = "native-ai-revoked-before-001"
	if response := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input); response.Code != 403 || f.model.calls.Load() != before {
		t.Fatal("revoked scope exported content")
	}
	approveMailboxAICloud(t, f.pool, f.principal.TenantID, true)
	f.model.hook = func() { approveMailboxAICloud(t, f.pool, f.principal.TenantID, false) }
	input["request_key"] = "native-ai-revoked-during-001"
	if response := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input); response.Code != 403 {
		t.Fatalf("inflight revoked output served status=%d", response.Code)
	}
	f.model.hook = nil
	approveMailboxAICloud(t, f.pool, f.principal.TenantID, true)
	f.model.hook = func() { f.mail.mu.Lock(); delete(f.mail.messages["INBOX"], 5); f.mail.mu.Unlock() }
	input["request_key"] = "native-ai-deleted-during-001"
	if response := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input); response.Code != 404 {
		t.Fatalf("deleted inflight source served output status=%d", response.Code)
	}
}

func TestNativeAINewDraftAndSourceCitationPG(t *testing.T) {
	f := newNativeAIHTTPFixture(t)
	token := loginNative(t, f.router, f.principal.Address, f.password)
	cfg, err := store.New(f.pool).PlatformGetAIProvider(t.Context(), f.principal.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	response := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", map[string]any{"operation": "draft_new", "configuration_digest": cfg.ConfigurationDigest, "confirmed": true, "request_key": "native-ai-new-draft-000001", "instruction": "Write a short invoice follow-up", "body": "Write a short invoice follow-up"})
	if response.Code != 200 {
		t.Fatalf("new author draft status=%d %s", response.Code, response.Body.String())
	}
	f.mail.mu.Lock()
	count := len(f.mail.messages["Drafts"])
	f.mail.mu.Unlock()
	if count != 0 {
		t.Fatal("preview persisted or sent without acceptance")
	}
	response = nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/search-ask", map[string]any{"question": "What is the invoice total?", "configuration_digest": cfg.ConfigurationDigest, "confirmed": true, "folder_enc": webmail.EncodeFolderName("INBOX")})
	if response.Code != 200 {
		t.Fatalf("ask %d %s", response.Code, response.Body.String())
	}
	var answer struct {
		Citations []string `json:"citations"`
		Grounded  bool     `json:"grounded"`
	}
	if json.Unmarshal(response.Body.Bytes(), &answer) != nil || !answer.Grounded || len(answer.Citations) != 1 {
		t.Fatal("grounded citation missing")
	}
	path := "/api/v1/mailbox/ai/citations/" + answer.Citations[0]
	citation := func() *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, req)
		return rec
	}
	if response = citation(); response.Code != 200 {
		t.Fatal("authorized citation did not resolve")
	}
	// A query outside this folder must not delete the canonical citation.
	filtered := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/search-ask", map[string]any{"question": "What is the invoice total?", "configuration_digest": cfg.ConfigurationDigest, "confirmed": true, "folder_enc": webmail.EncodeFolderName("Drafts")})
	if filtered.Code != 200 || !strings.Contains(filtered.Body.String(), `"grounded":false`) {
		t.Fatalf("empty scope answer: %d %s", filtered.Code, filtered.Body.String())
	}
	if response = citation(); response.Code != 200 {
		t.Fatalf("filtered query deleted canonical citation: %d", response.Code)
	}
	f.mail.mu.Lock()
	delete(f.mail.messages["INBOX"], 5)
	f.mail.mu.Unlock()
	if response = citation(); response.Code != 404 {
		t.Fatalf("deleted citation still readable %d", response.Code)
	}
}

func TestNativeAIScopeMigrationRequiresRenewedConsentPG(t *testing.T) {
	p, principal := mailboxAIFixture(t, 10)
	q := store.New(p)
	cfg, err := q.PlatformGetAIProvider(t.Context(), principal.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	oldDigest := cfg.ConfigurationDigest
	for _, direction := range []string{"down", "up"} {
		raw, err := os.ReadFile("../../../migrations/0136_ai_native_scope." + direction + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = p.Exec(t.Context(), string(raw)); err != nil {
			t.Fatal(err)
		}
		cfg, err = q.PlatformGetAIProvider(t.Context(), principal.TenantID)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ConfigurationDigest == oldDigest {
			t.Fatal("migration failed to rotate exact approved target")
		}
		oldDigest = cfg.ConfigurationDigest
		allowed, err := q.AICloudAuthorized(t.Context(), store.AICloudAuthorizedParams{TenantID: principal.TenantID, ConfigurationDigest: cfg.ConfigurationDigest, ScopeHash: cfg.CloudScopeHash})
		if err != nil || allowed {
			t.Fatalf("migration silently reused old approval: allowed=%v err=%v", allowed, err)
		}
	}
	approveMailboxAICloud(t, p, principal.TenantID, true)
	cfg, err = q.PlatformGetAIProvider(t.Context(), principal.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := q.AICloudAuthorized(t.Context(), store.AICloudAuthorizedParams{TenantID: principal.TenantID, ConfigurationDigest: cfg.ConfigurationDigest, ScopeHash: AICloudScopeHash})
	if err != nil || !allowed {
		t.Fatal("explicit renewed authorization did not restore access")
	}
}

func TestNativeAIPreviewBindsActualContentPG(t *testing.T) {
	f := newNativeAIHTTPFixture(t)
	token := loginNative(t, f.router, f.principal.Address, f.password)
	cfg, err := store.New(f.pool).PlatformGetAIProvider(t.Context(), f.principal.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"operation": "summarize", "configuration_digest": cfg.ConfigurationDigest, "confirmed": false, "preview_only": true, "request_key": "content-preview-proof-0001", "body": "Only this selected content", "subject": "Invoice"}
	preview := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input)
	var value struct {
		Digest   string `json:"content_digest"`
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if preview.Code != 200 || json.Unmarshal(preview.Body.Bytes(), &value) != nil || value.Digest == "" || f.model.calls.Load() != 0 {
		t.Fatalf("preview exported content: %d %s", preview.Code, preview.Body.String())
	}
	input["preview_only"] = false
	input["confirmed"] = true
	input["content_digest"] = value.Digest
	input["body"] = "Different unconfirmed content"
	if response := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input); response.Code != 409 || f.model.calls.Load() != 0 {
		t.Fatal("changed content was exported")
	}
	input["body"] = "Only this selected content"
	if response := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input); response.Code != 200 || f.model.calls.Load() != 1 {
		t.Fatalf("confirmed content failed: %d %s", response.Code, response.Body.String())
	}
}

func TestNativeAIModelsRejectUnapprovedLoopbackPG(t *testing.T) {
	f := newNativeAIHTTPFixture(t)
	token := loginNative(t, f.router, f.principal.Address, f.password)
	var calls atomic.Int32
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"data":[{"id":"private-model"}]}`))
	}))
	defer private.Close()
	response := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/models", map[string]any{"provider": "ollama", "endpoint": private.URL, "model": "private"})
	if response.Code != 400 || calls.Load() != 0 {
		t.Fatalf("unapproved local endpoint accessed: status=%d calls=%d", response.Code, calls.Load())
	}
}

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/ai"
	"github.com/bushixiucai/urmail/internal/rendercache"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// TestNativeAIAssistPhishingFindingPersistedAndExposedPG pins the v0.6.15
// verdict contract: a phishing_v2 analysis through the existing assist
// operation upserts exactly one row per mailbox location, the GetMessage
// payload carries the phishing object for analyzed mail and omits the key
// for unanalyzed mail, and a cached pre-analysis rendering is evicted so
// the verdict shows up on the next open instead of after the render TTL.
func TestNativeAIAssistPhishingFindingPersistedAndExposedPG(t *testing.T) {
	f := newNativeAIHTTPFixture(t)
	token := loginNative(t, f.router, f.principal.Address, f.password)
	cfg, err := store.New(f.pool).PlatformGetAIProvider(t.Context(), f.principal.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	f.native.RenderCache = rendercache.New()
	const analyzedPath = "/api/v1/mailbox/folders/INBOX/messages/5?uid_validity=9"

	pre := getMessageJSON(t, f, token, analyzedPath)
	if pre.Code != http.StatusOK || strings.Contains(pre.Body.String(), `"phishing"`) {
		t.Fatalf("pre-analysis message should carry no phishing key: %d %s", pre.Code, pre.Body.String())
	}
	if pre.Header().Get("X-Render-Cache") != "miss" {
		t.Fatal("first message read should render fresh")
	}

	f.model.respond = func(_ ai.CompletionRequest) (string, bool) {
		raw, _ := json.Marshal(map[string]any{
			"score":      0.9,
			"risk_level": "enum-ignored-by-server",
			"reasons":    []string{"Lookalike sender domain."},
			"indicators": []map[string]any{{"type": "spoofed_domain", "evidence": "paypa1-verify.center"}},
		})
		return string(raw), true
	}
	first := map[string]any{"operation": "phishing", "configuration_digest": cfg.ConfigurationDigest, "confirmed": true, "request_key": "native-ai-phishing-finding-000001", "folder_enc": webmail.EncodeFolderName("INBOX"), "uid": 5, "uid_validity": 9, "selection": "The October invoice total is 42 euros."}
	if response := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", first); response.Code != http.StatusOK {
		t.Fatalf("phishing assist failed: %d %s", response.Code, response.Body.String())
	}

	var count int
	if err := f.pool.QueryRow(t.Context(), "SELECT count(*) FROM mailbox_ai_phishing_findings WHERE mailbox_id=$1 AND folder='INBOX' AND uid_validity=9 AND uid=5", f.principal.MailboxID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("finding rows = %d, err=%v, want exactly 1", count, err)
	}

	post := getMessageJSON(t, f, token, analyzedPath)
	if post.Code != http.StatusOK || post.Header().Get("X-Render-Cache") != "miss" {
		t.Fatalf("cached pre-analysis rendering survived the analysis: %d %s", post.Code, post.Header().Get("X-Render-Cache"))
	}
	var analyzed struct {
		Phishing *struct {
			Score      float64  `json:"score"`
			RiskLevel  string   `json:"risk_level"`
			Reasons    []string `json:"reasons"`
			Indicators []struct {
				Type     string `json:"type"`
				Evidence string `json:"evidence"`
			} `json:"indicators"`
			AnalyzedAt string `json:"analyzed_at"`
		} `json:"phishing"`
	}
	if err := json.Unmarshal(post.Body.Bytes(), &analyzed); err != nil {
		t.Fatalf("message payload not JSON: %v", err)
	}
	if analyzed.Phishing == nil {
		t.Fatalf("analyzed message carries no phishing object: %s", post.Body.String())
	}
	if analyzed.Phishing.Score != 0.9 || analyzed.Phishing.RiskLevel != "critical" {
		t.Fatalf("verdict score/risk = %v/%q, want 0.9/critical (score-mapped)", analyzed.Phishing.Score, analyzed.Phishing.RiskLevel)
	}
	if len(analyzed.Phishing.Indicators) != 1 || analyzed.Phishing.Indicators[0].Type != "spoofed_domain" || analyzed.Phishing.Indicators[0].Evidence != "paypa1-verify.center" {
		t.Fatalf("indicators = %+v, want the spoofed_domain evidence", analyzed.Phishing.Indicators)
	}
	if analyzed.Phishing.AnalyzedAt == "" {
		t.Fatal("analyzed_at missing")
	}

	f.mail.messages["INBOX"][6] = &webmail.Message{MessageSummary: webmail.MessageSummary{UID: 6, Subject: "Normal note", From: []webmail.Address{{Email: "friend@example.test"}}}, UIDValidity: 9, Text: "Nothing suspicious here."}
	plain := getMessageJSON(t, f, token, "/api/v1/mailbox/folders/INBOX/messages/6?uid_validity=9")
	if plain.Code != http.StatusOK || strings.Contains(plain.Body.String(), `"phishing"`) {
		t.Fatalf("unanalyzed message must omit the phishing key: %d %s", plain.Code, plain.Body.String())
	}

	f.model.respond = func(_ ai.CompletionRequest) (string, bool) {
		return `{"score":0.1,"risk_level":"low","reasons":[],"indicators":[]}`, true
	}
	second := map[string]any{"operation": "phishing", "configuration_digest": cfg.ConfigurationDigest, "confirmed": true, "request_key": "native-ai-phishing-finding-000002", "folder_enc": webmail.EncodeFolderName("INBOX"), "uid": 5, "uid_validity": 9, "selection": "The October invoice total is 42 euros."}
	if response := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", second); response.Code != http.StatusOK {
		t.Fatalf("re-analysis failed: %d %s", response.Code, response.Body.String())
	}
	if err := f.pool.QueryRow(t.Context(), "SELECT count(*) FROM mailbox_ai_phishing_findings WHERE mailbox_id=$1 AND folder='INBOX' AND uid_validity=9 AND uid=5", f.principal.MailboxID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("re-analysis should upsert, rows = %d err=%v, want 1", count, err)
	}
	updated := getMessageJSON(t, f, token, analyzedPath)
	var refreshed struct {
		Phishing *struct {
			Score     float64 `json:"score"`
			RiskLevel string  `json:"risk_level"`
		} `json:"phishing"`
	}
	if err := json.Unmarshal(updated.Body.Bytes(), &refreshed); err != nil {
		t.Fatalf("re-analysis payload not JSON: %v", err)
	}
	if refreshed.Phishing == nil || refreshed.Phishing.Score != 0.1 || refreshed.Phishing.RiskLevel != "low" {
		t.Fatalf("re-analysis verdict = %+v, want 0.1/low", refreshed.Phishing)
	}
}

func getMessageJSON(t *testing.T, f nativeAIHTTPFixture, token, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

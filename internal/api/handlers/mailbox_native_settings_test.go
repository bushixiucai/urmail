package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/sieve"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type fakeSelfSettings struct {
	settings    MailboxSettings
	quotaUsed   int64
	quotaTotal  int64
	identities  []string
	allowedFrom map[string]bool
	saves       []MailboxSettingsSave
}

func (f *fakeSelfSettings) Settings(context.Context, int64, int64) (MailboxSettings, error) {
	return f.settings, nil
}

func (f *fakeSelfSettings) SaveSettings(_ context.Context, save MailboxSettingsSave) (int64, error) {
	if save.ExpectedVersion != f.settings.Version {
		return 0, ErrSettingsConflict
	}
	f.saves = append(f.saves, save)
	f.settings.Version++
	return f.settings.Version, nil
}

func (f *fakeSelfSettings) Quota(context.Context, int64, int64) (int64, int64, error) {
	return f.quotaUsed, f.quotaTotal, nil
}

func (f *fakeSelfSettings) SenderIdentities(context.Context, int64, string) ([]string, error) {
	return f.identities, nil
}

func (f *fakeSelfSettings) SenderIdentityAllowed(_ context.Context, _ int64, _, from string) (bool, error) {
	return f.allowedFrom[from], nil
}

type fakeRulesStore struct {
	script, mode string
	saved        string
}

func (f *fakeRulesStore) Sieve(context.Context, int64, int64) (string, string, error) {
	return f.script, f.mode, nil
}

func (f *fakeRulesStore) SaveSieve(_ context.Context, _, _ int64, script, _ string) (bool, error) {
	f.saved = script
	return true, nil
}

type fakeActivator struct{ calls int }

func (f *fakeActivator) ActivateSieve(context.Context, int64, int64, string) error {
	f.calls++
	return nil
}

func newSettingsTestRouter(h *MailboxNativeHandler, sessions middleware.MailboxSessionLookup) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	auth := r.Group("/api/v1/mailbox")
	auth.Use(middleware.RequireMailboxAuth(sessions))
	auth.GET("/settings", h.GetSettings)
	auth.PUT("/settings", h.PutSettings)
	auth.GET("/identities", h.ListIdentities)
	auth.GET("/quota", h.GetQuota)
	auth.GET("/rules", h.GetRules)
	auth.PUT("/rules", h.PutRules)
	auth.POST("/send", h.SendMessage)
	return r
}

func settingsRequest(t *testing.T, r *gin.Engine, token, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequestWithContext(context.Background(), method, path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestMailboxSettingsOptimisticConflictAndHappyPath(t *testing.T) {
	loginAttempts = newLoginLimiter()
	token := "settings-token-0001"
	hash := mailboxprincipal.HashToken(token)
	sessions := &fakeMailboxSessions{byHash: map[string]webmail.Session{hash: {TokenHash: hash, TenantID: 7, MailboxID: 9, Address: "alice@example.test", ExpiresAt: time.Now().Add(time.Hour)}}}
	store := &fakeSelfSettings{settings: MailboxSettings{Version: 3, Preferences: json.RawMessage(`{"thread":true}`)}}
	h := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{}, Sessions: sessions, SelfSettings: store}
	r := newSettingsTestRouter(h, sessions)

	w := settingsRequest(t, r, token, http.MethodGet, "/api/v1/mailbox/settings", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get settings %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Version     int64           `json:"version"`
		Preferences json.RawMessage `json:"preferences"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Version != 3 {
		t.Fatalf("decode settings %v %s", err, w.Body.String())
	}

	// Stale optimistic version: 409, nothing persisted.
	w = settingsRequest(t, r, token, http.MethodPut, "/api/v1/mailbox/settings", map[string]any{
		"display_name": "Alice", "signature_text": "hi", "expected_version": 2,
	})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "MAILBOX_SETTINGS_VERSION_CONFLICT") {
		t.Fatalf("stale save %d %s", w.Code, w.Body.String())
	}
	if len(store.saves) != 0 {
		t.Fatalf("conflicting save persisted: %+v", store.saves)
	}

	w = settingsRequest(t, r, token, http.MethodPut, "/api/v1/mailbox/settings", map[string]any{
		"display_name": "Alice", "signature_text": "hi", "signature_html": "<b>hi</b>", "expected_version": 3,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("save settings %d %s", w.Code, w.Body.String())
	}
	var saved struct {
		Version int64 `json:"version"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || saved.Version != 4 {
		t.Fatalf("decode save %v %s", err, w.Body.String())
	}

	// Oversized signature is rejected before the store sees it.
	w = settingsRequest(t, r, token, http.MethodPut, "/api/v1/mailbox/settings", map[string]any{
		"signature_text": strings.Repeat("x", 9000), "expected_version": 4,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized signature %d", w.Code)
	}
}

func TestMailboxFromIdentityAuthorization(t *testing.T) {
	loginAttempts = newLoginLimiter()
	token := "identity-token-0001"
	hash := mailboxprincipal.HashToken(token)
	sessions := &fakeMailboxSessions{byHash: map[string]webmail.Session{hash: {TokenHash: hash, TenantID: 7, MailboxID: 9, Address: "alice@example.test", ExpiresAt: time.Now().Add(time.Hour)}}}
	vault := webmail.NewMemoryVault()
	vault.Put(hash, "pw")
	submit := &fakeSubmitter{}
	store := &fakeSelfSettings{allowedFrom: map[string]bool{"team@example.test": true}}
	h := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{}, Sessions: sessions, Vault: vault, Submitter: submit, SelfSettings: store}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.POST("/api/v1/mailbox/send", func(c *gin.Context) {
		var req struct {
			From string `json:"from"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			t.Fatal(err)
		}
		_, header, ok := h.authorizedIdentity(c, sessions.byHash[hash], req.From)
		if !ok {
			return
		}
		submit.last.From = header
		c.Status(200)
	})
	send := func(from string) *httptest.ResponseRecorder {
		return settingsRequest(t, r, token, http.MethodPost, "/api/v1/mailbox/send", map[string]any{"from": from})
	}

	// Unauthorized From is rejected server-side, never trusted from the client.
	if w := send("evil@attacker.test"); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "MAILBOX_FROM_NOT_AUTHORIZED") {
		t.Fatalf("unauthorized from %d %s", w.Code, w.Body.String())
	}
	if submit.calls != 0 {
		t.Fatalf("rejected message still submitted")
	}
	// A reviewed alias-sender identity goes through with that From header.
	if w := send("team@example.test"); w.Code != http.StatusOK {
		t.Fatalf("authorized from %d %s", w.Code, w.Body.String())
	}
	if submit.last.From != "team@example.test" {
		t.Fatalf("submitted From %q", submit.last.From)
	}
	// Default stays the primary address.
	if w := send(""); w.Code != http.StatusOK || submit.last.From != "alice@example.test" {
		t.Fatalf("default from %d %q", w.Code, submit.last.From)
	}
	// Without any authorization source, a non-primary From fails closed.
	h.SelfSettings = nil
	if w := send("team@example.test"); w.Code != http.StatusForbidden {
		t.Fatalf("fail-closed from %d", w.Code)
	}
}

func TestMailboxQuotaAndIdentities(t *testing.T) {
	loginAttempts = newLoginLimiter()
	token := "quota-token-0001"
	hash := mailboxprincipal.HashToken(token)
	sessions := &fakeMailboxSessions{byHash: map[string]webmail.Session{hash: {TokenHash: hash, TenantID: 7, MailboxID: 9, Address: "alice@example.test", ExpiresAt: time.Now().Add(time.Hour)}}}
	store := &fakeSelfSettings{quotaUsed: 1234, quotaTotal: 4096, identities: []string{"team@example.test"}}
	h := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{}, Sessions: sessions, SelfSettings: store}
	r := newSettingsTestRouter(h, sessions)

	w := settingsRequest(t, r, token, http.MethodGet, "/api/v1/mailbox/quota", nil)
	var quota struct {
		UsedBytes  int64 `json:"used_bytes"`
		QuotaBytes int64 `json:"quota_bytes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &quota); err != nil || quota.UsedBytes != 1234 || quota.QuotaBytes != 4096 {
		t.Fatalf("quota %d %s %v", w.Code, w.Body.String(), err)
	}

	w = settingsRequest(t, r, token, http.MethodGet, "/api/v1/mailbox/identities", nil)
	var ids struct {
		Identities []struct {
			Address string `json:"address"`
			Primary bool   `json:"primary"`
		} `json:"identities"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ids); err != nil || len(ids.Identities) != 2 {
		t.Fatalf("identities %d %s %v", w.Code, w.Body.String(), err)
	}
	if !ids.Identities[0].Primary || ids.Identities[0].Address != "alice@example.test" {
		t.Fatalf("primary identity %+v", ids.Identities[0])
	}
}

func TestMailboxRulesSelfService(t *testing.T) {
	loginAttempts = newLoginLimiter()
	token := "rules-token-0001"
	hash := mailboxprincipal.HashToken(token)
	sessions := &fakeMailboxSessions{byHash: map[string]webmail.Session{hash: {TokenHash: hash, TenantID: 7, MailboxID: 9, Address: "alice@example.test", ExpiresAt: time.Now().Add(time.Hour)}}}
	rulesStore := &fakeRulesStore{mode: "managed", script: ""}
	activator := &fakeActivator{}
	h := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{}, Sessions: sessions, Rules: rulesStore, Sieve: activator}
	r := newSettingsTestRouter(h, sessions)

	// An empty managed script decodes as the empty rule set with its digest.
	w := settingsRequest(t, r, token, http.MethodGet, "/api/v1/mailbox/rules", nil)
	var view struct {
		Version string      `json:"version"`
		Rules   sieve.Rules `json:"rules"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || view.Version != sieve.Digest("") {
		t.Fatalf("get rules %d %s %v", w.Code, w.Body.String(), err)
	}

	// Unconfirmed write is refused.
	w = settingsRequest(t, r, token, http.MethodPut, "/api/v1/mailbox/rules", map[string]any{"confirmed": false, "expected_version": view.Version, "rules": sieve.Rules{}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed rules %d", w.Code)
	}
	// Wrong digest is a conflict.
	w = settingsRequest(t, r, token, http.MethodPut, "/api/v1/mailbox/rules", map[string]any{"confirmed": true, "expected_version": strings.Repeat("0", 64), "rules": sieve.Rules{}})
	if w.Code != http.StatusConflict {
		t.Fatalf("stale rules %d", w.Code)
	}

	next := sieve.Rules{
		Rules:      []sieve.Rule{{Header: "subject", Match: "contains", Value: "invoice", Action: "fileinto", Folder: "Finance"}},
		Forwarding: &sieve.Forwarding{Enabled: true, Target: "archive@example.test", KeepCopy: true},
	}
	w = settingsRequest(t, r, token, http.MethodPut, "/api/v1/mailbox/rules", map[string]any{"confirmed": true, "expected_version": view.Version, "rules": next})
	if w.Code != http.StatusOK {
		t.Fatalf("put rules %d %s", w.Code, w.Body.String())
	}
	var applied struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &applied); err != nil || applied.State != "active" {
		t.Fatalf("decode apply %v %s", err, w.Body.String())
	}
	if activator.calls != 1 {
		t.Fatalf("activation calls %d", activator.calls)
	}
	if !strings.Contains(rulesStore.saved, "redirect :copy \"archive@example.test\";") {
		t.Fatalf("saved script %s", rulesStore.saved)
	}
	if _, err := sieve.Decode(rulesStore.saved); err != nil {
		t.Fatalf("saved script does not round trip: %v", err)
	}

	// Legacy ManageSieve mode is never written from the user path.
	rulesStore.mode = "legacy_v80"
	w = settingsRequest(t, r, token, http.MethodPut, "/api/v1/mailbox/rules", map[string]any{"confirmed": true, "expected_version": view.Version, "rules": next})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "SIEVE_LEGACY_MANAGESIEVE_REQUIRED") {
		t.Fatalf("legacy write %d %s", w.Code, w.Body.String())
	}
}

package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/domain/mailboxsettings"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// remoteImagesSettingsHarness builds one authenticated webmail settings route
// pair over a fake store preloaded with the given stored preferences.
func remoteImagesSettingsHarness(t *testing.T, storedPreferences string) (*gin.Engine, *fakeSelfSettings, string) {
	t.Helper()
	loginAttempts = newLoginLimiter()
	token := "remote-images-token-0001"
	hash := mailboxprincipal.HashToken(token)
	sessions := &fakeMailboxSessions{byHash: map[string]webmail.Session{hash: {TokenHash: hash, TenantID: 7, MailboxID: 9, Address: "alice@example.test", ExpiresAt: time.Now().Add(time.Hour)}}}
	store := &fakeSelfSettings{settings: MailboxSettings{Version: 5, Preferences: json.RawMessage(storedPreferences)}}
	h := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{}, Sessions: sessions, SelfSettings: store}
	r := newSettingsTestRouter(h, sessions)
	return r, store, token
}

func savedPreferencesOf(t *testing.T, store *fakeSelfSettings) map[string]json.RawMessage {
	t.Helper()
	if len(store.saves) != 1 {
		t.Fatalf("saves %d, want exactly 1", len(store.saves))
	}
	var prefs map[string]json.RawMessage
	if err := json.Unmarshal(store.saves[0].Preferences, &prefs); err != nil {
		t.Fatalf("saved prefs %s: %v", store.saves[0].Preferences, err)
	}
	return prefs
}

func boolPref(raw json.RawMessage) (bool, bool) {
	var enabled bool
	if err := json.Unmarshal(raw, &enabled); err != nil {
		return false, false
	}
	return enabled, true
}

// Old clients PUT the whole preferences object without knowing the new key:
// the server must preserve the stored allow_all_remote_images value.
func TestMailboxSettingsPutPreservesAllowAllRemoteImages(t *testing.T) {
	r, store, token := remoteImagesSettingsHarness(t, `{"allow_all_remote_images":true,"thread":true}`)
	w := settingsRequest(t, r, token, http.MethodPut, "/api/v1/mailbox/settings", map[string]any{
		"display_name": "Alice", "expected_version": 5,
		"preferences": map[string]any{"thread": true},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("legacy put %d %s", w.Code, w.Body.String())
	}
	prefs := savedPreferencesOf(t, store)
	enabled, ok := boolPref(prefs["allow_all_remote_images"])
	if !ok || !enabled {
		t.Fatalf("legacy put lost allow_all_remote_images: %s", store.saves[0].Preferences)
	}
	if _, ok := prefs["thread"]; !ok {
		t.Fatal("legacy put dropped an unrelated existing key")
	}
}

// A mailbox that never opted in keeps defaulting to absent (read as false):
// no value must be invented by the server on a legacy PUT.
func TestMailboxSettingsPutDefaultStaysAbsent(t *testing.T) {
	r, store, token := remoteImagesSettingsHarness(t, `{"thread":true}`)
	w := settingsRequest(t, r, token, http.MethodPut, "/api/v1/mailbox/settings", map[string]any{
		"display_name": "Alice", "expected_version": 5,
		"preferences": map[string]any{"density": "cozy"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("default put %d %s", w.Code, w.Body.String())
	}
	prefs := savedPreferencesOf(t, store)
	if raw, existed := prefs["allow_all_remote_images"]; existed {
		t.Fatalf("default put invented allow_all_remote_images: %s", raw)
	}
	if _, ok := prefs["density"]; !ok {
		t.Fatal("new keys must still be stored")
	}
}

// An explicit write round-trips both states; an invalid typed value is a 400.
func TestMailboxSettingsPutExplicitValuesAndInvalid(t *testing.T) {
	r, store, token := remoteImagesSettingsHarness(t, `{}`)
	for _, want := range []bool{true, false} {
		w := settingsRequest(t, r, token, http.MethodPut, "/api/v1/mailbox/settings", map[string]any{
			"display_name": "Alice", "expected_version": store.settings.Version,
			"preferences": map[string]any{"allow_all_remote_images": want},
		})
		if w.Code != http.StatusOK {
			t.Fatalf("explicit %v put %d %s", want, w.Code, w.Body.String())
		}
		enabled, ok := boolPref(savedPreferencesOf(t, store)["allow_all_remote_images"])
		if !ok || enabled != want {
			t.Fatalf("explicit %v not stored: %s", want, store.saves[0].Preferences)
		}
		store.saves = nil
	}
	for _, bad := range []any{nil, "true", 1, []any{}, map[string]any{}} {
		w := settingsRequest(t, r, token, http.MethodPut, "/api/v1/mailbox/settings", map[string]any{
			"display_name": "Alice", "expected_version": store.settings.Version,
			"preferences": map[string]any{"allow_all_remote_images": bad},
		})
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "MAILBOX_SETTINGS_PREFERENCES_INVALID") {
			t.Fatalf("invalid %v value %d %s", bad, w.Code, w.Body.String())
		}
		store.saves = nil
	}
}

// A stale optimistic version is rejected before any preference merge happens:
// nothing is persisted and the stored true value stays untouched.
func TestMailboxSettingsPutConflictKeepsRemoteImages(t *testing.T) {
	r, store, token := remoteImagesSettingsHarness(t, `{"allow_all_remote_images":true}`)
	w := settingsRequest(t, r, token, http.MethodPut, "/api/v1/mailbox/settings", map[string]any{
		"display_name": "Alice", "expected_version": 4,
		"preferences": map[string]any{"allow_all_remote_images": false},
	})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "MAILBOX_SETTINGS_VERSION_CONFLICT") {
		t.Fatalf("conflicting put %d %s", w.Code, w.Body.String())
	}
	if len(store.saves) != 0 {
		t.Fatalf("conflicting put persisted: %+v", store.saves)
	}
	if store.settings.Version != 5 {
		t.Fatalf("conflicting put moved version to %d", store.settings.Version)
	}
}

// Unknown keys stay scalar-bounded and the ai preference survives a legacy PUT
// that omits it, exactly as before this key existed.
func TestMailboxSettingsPutKeepsUnknownKeysAndAI(t *testing.T) {
	box, err := secrets.New([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	loginAttempts = newLoginLimiter()
	token := "remote-images-ai-token-001"
	hash := mailboxprincipal.HashToken(token)
	sessions := &fakeMailboxSessions{byHash: map[string]webmail.Session{hash: {TokenHash: hash, TenantID: 7, MailboxID: 9, Address: "alice@example.test", ExpiresAt: time.Now().Add(time.Hour)}}}
	stored, err := mailboxsettings.Preferences(json.RawMessage(`{"ai":{"use_custom":true,"provider":"openai","endpoint":"https://api.example.test/v1","model":"example","api_key":"private-provider-key"},"allow_all_remote_images":true,"weird":"ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	protected, err := (&MailboxNativeHandler{Secrets: box}).protectMailboxAI(stored, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeSelfSettings{settings: MailboxSettings{Version: 5, Preferences: protected}}
	h := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{}, Sessions: sessions, SelfSettings: store, Secrets: box}
	r := newSettingsTestRouter(h, sessions)

	w := settingsRequest(t, r, token, http.MethodPut, "/api/v1/mailbox/settings", map[string]any{
		"display_name": "Alice", "expected_version": 5,
		"preferences": map[string]any{"thread": true},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("legacy put with ai %d %s", w.Code, w.Body.String())
	}
	prefs := savedPreferencesOf(t, store)
	if raw, ok := prefs["weird"]; !ok || string(raw) != `"ok"` {
		t.Fatalf("unknown scalar key lost: %s", store.saves[0].Preferences)
	}
	var saved struct {
		AI mailboxsettings.AIConfig `json:"ai"`
	}
	if json.Unmarshal(store.saves[0].Preferences, &saved) != nil || saved.AI.Provider != "openai" {
		t.Fatalf("ai preference lost or mangled: %s", store.saves[0].Preferences)
	}
	kept, err := box.DecryptString(saved.AI.APIKey)
	if err != nil || kept != "private-provider-key" {
		t.Fatalf("ai credential not preserved: %q %v", kept, err)
	}
	enabled, ok := boolPref(prefs["allow_all_remote_images"])
	if !ok || !enabled {
		t.Fatalf("allow_all_remote_images lost on legacy put: %s", store.saves[0].Preferences)
	}
	// Nested objects for unknown keys remain rejected.
	w = settingsRequest(t, r, token, http.MethodPut, "/api/v1/mailbox/settings", map[string]any{
		"display_name": "Alice", "expected_version": store.settings.Version,
		"preferences": map[string]any{"nested": map[string]any{"deep": 1}},
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "MAILBOX_SETTINGS_PREFERENCES_INVALID") {
		t.Fatalf("nested unknown key %d %s", w.Code, w.Body.String())
	}
}

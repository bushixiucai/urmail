package handlers

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/domain/mailboxsettings"
	"github.com/bushixiucai/urmail/internal/secrets"
)

func TestMailboxAIWriteOnlyKeyLifecycle(t *testing.T) {
	box, err := secrets.New([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	h := &MailboxNativeHandler{Secrets: box}
	input := json.RawMessage(`{"timezone":"Asia/Shanghai","ai":{"use_custom":true,"provider":"openai","endpoint":"https://api.example.test/v1","model":"example","api_key":"private-provider-key"}}`)
	if _, err = mailboxsettings.Preferences(input); err != nil {
		t.Fatal(err)
	}
	stored, err := h.protectMailboxAI(input, nil)
	if err != nil || strings.Contains(string(stored), "private-provider-key") || !strings.Contains(string(stored), "enc:v1:") {
		t.Fatalf("key not protected: %v", err)
	}
	public := publicMailboxPreferences(stored)
	if strings.Contains(string(public), "enc:v1:") || strings.Contains(string(public), "private-provider-key") || !strings.Contains(string(public), `"key_configured":true`) {
		t.Fatal("key exposed or configuration lost")
	}
	kept, err := h.protectMailboxAI(public, stored)
	if err != nil {
		t.Fatal(err)
	}
	var prefs struct {
		AI mailboxsettings.AIConfig `json:"ai"`
	}
	if json.Unmarshal(kept, &prefs) != nil {
		t.Fatal("invalid saved settings")
	}
	key, err := box.DecryptString(prefs.AI.APIKey)
	if err != nil || key != "private-provider-key" {
		t.Fatal("blank key did not retain previous secret")
	}
	if _, err = h.protectMailboxAI(json.RawMessage(`{"ai":{"use_custom":true,"provider":"openai","endpoint":"https://different.example.test/v1","model":"example"}}`), kept); !errors.Is(err, mailboxsettings.ErrAICredentialTarget) {
		t.Fatal("saved key followed an unconfirmed destination change")
	}
	cleared, err := h.protectMailboxAI(json.RawMessage(`{"ai":{"use_custom":false,"clear_api_key":true}}`), kept)
	if err != nil || strings.Contains(string(cleared), "enc:v1:") {
		t.Fatal("explicit clear did not remove secret")
	}
	if _, err = (&MailboxNativeHandler{}).protectMailboxAI(input, nil); err == nil {
		t.Fatal("missing encryption silently accepted plaintext")
	}
}

func TestMailboxSettingsAcceptBoundedAIAndRejectOtherObjects(t *testing.T) {
	for _, raw := range []string{`null`, `{"ai":{"unknown":1}}`, `{"ai":{"use_custom":true,"endpoint":"http://api.example.test","model":"x"}}`, `{"nested":{"field":1}}`, `{"ai":{"api_key":"x","clear_api_key":true}}`} {
		if _, err := mailboxsettings.Preferences(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted invalid preference: %s", raw)
		}
	}
	store := &fakeSelfSettings{settings: MailboxSettings{Version: 0, Preferences: json.RawMessage(`{}`)}}
	h := &MailboxNativeHandler{Enabled: true, SelfSettings: store}
	// The currently shipped page includes an empty disabled AI object on every save.
	prefs, err := mailboxsettings.Preferences(json.RawMessage(`{"timezone":"Asia/Shanghai","ai":{"use_custom":false,"provider":"openai","endpoint":"","model":"","api_key":""}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.protectMailboxAI(prefs, nil); err != nil {
		t.Fatal(err)
	}
}

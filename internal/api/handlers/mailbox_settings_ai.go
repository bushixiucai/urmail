package handlers

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/bushixiucai/urmail/internal/ai"
	"github.com/bushixiucai/urmail/internal/domain/mailboxsettings"
)

// publicMailboxPreferences never exposes stored provider credentials.
func publicMailboxPreferences(raw json.RawMessage) json.RawMessage {
	var prefs map[string]json.RawMessage
	if json.Unmarshal(raw, &prefs) != nil || prefs == nil {
		return json.RawMessage("{}")
	}
	if value, ok := prefs["ai"]; ok {
		var cfg mailboxsettings.AIConfig
		if json.Unmarshal(value, &cfg) != nil {
			delete(prefs, "ai")
		} else {
			cfg.KeyConfigured = cfg.APIKey != ""
			cfg.APIKey = ""
			cfg.ClearAPIKey = false
			prefs["ai"], _ = json.Marshal(cfg) //nolint:gosec // G117: APIKey is explicitly cleared above and omitted from this public projection.
		}
	}
	result, _ := json.Marshal(prefs)
	return result
}

// protectMailboxAI preserves blank write-only keys and encrypts replacements.
// A missing AI preference preserves the stored configuration for older clients.
func (h *MailboxNativeHandler) protectMailboxAI(raw, previous json.RawMessage) (json.RawMessage, error) {
	var prefs, old map[string]json.RawMessage
	if err := json.Unmarshal(raw, &prefs); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(previous, &old)
	value, exists := prefs["ai"]
	if !exists {
		if oldAI, found := old["ai"]; found {
			value, exists = oldAI, true
		}
	}
	if !exists {
		return raw, nil
	}
	cfg, err := mailboxsettings.ParseAI(value)
	if err != nil {
		return nil, err
	}
	var oldConfig mailboxsettings.AIConfig
	_ = json.Unmarshal(old["ai"], &oldConfig)
	secret := cfg.APIKey
	if secret == "" && !cfg.ClearAPIKey {
		if oldConfig.APIKey != "" && (oldConfig.Provider != cfg.Provider || strings.TrimRight(oldConfig.Endpoint, "/") != strings.TrimRight(cfg.Endpoint, "/")) {
			return nil, mailboxsettings.ErrAICredentialTarget
		}
		secret = oldConfig.APIKey
	}
	if secret != "" {
		if h.Secrets == nil {
			return nil, errors.New("mailbox AI encryption unavailable")
		}
		if strings.HasPrefix(secret, "enc:v1:") {
			if cfg.APIKey != "" && secret != oldConfig.APIKey {
				return nil, mailboxsettings.ErrPreferences
			}
			secret, err = h.Secrets.DecryptString(secret)
			if err != nil {
				return nil, err
			}
		}
	}
	if cfg.UseCustom {
		if cfg.Provider == ai.ProviderOllama {
			if _, err = ai.ConfiguredProvider(cfg.Provider, cfg.Endpoint, cfg.Model, "", nil, h.AILocalEndpoints); err != nil {
				return nil, mailboxsettings.ErrPreferences
			}
		} else if _, err = ai.DynamicProvider(cfg.Provider, cfg.Endpoint, cfg.Model, secret); err != nil {
			return nil, mailboxsettings.ErrPreferences
		}
	}
	cfg.APIKey, cfg.ClearAPIKey, cfg.KeyConfigured = "", false, false
	if secret != "" {
		cfg.APIKey, err = h.Secrets.EncryptString(secret)
		if err != nil || !strings.HasPrefix(cfg.APIKey, "enc:v1:") {
			return nil, errors.New("mailbox AI encryption failed")
		}
	}
	prefs["ai"], err = json.Marshal(cfg) //nolint:gosec // G117: APIKey contains only authenticated ciphertext verified above, never a plaintext credential.
	if err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(prefs)
	if err != nil || len(normalized) > mailboxsettings.MaxPreferencesBytes {
		return nil, mailboxsettings.ErrPreferences
	}
	return normalized, nil
}

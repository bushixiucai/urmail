package mailboxsettings

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unicode"
)

// ErrAICredentialTarget prevents a retained key from being sent to a new destination.
var ErrAICredentialTarget = errors.New("MAILBOX_AI_KEY_TARGET_CHANGED")

// ErrAIPersonalBYOKDisabled is returned when personal BYOK is not allowed by governance.
var ErrAIPersonalBYOKDisabled = errors.New("AI_PERSONAL_BYOK_DISABLED")

// ErrAIEndpointNotApproved is returned when the endpoint is outside the approved allowlist.
var ErrAIEndpointNotApproved = errors.New("AI_ENDPOINT_NOT_APPROVED")

// AIConfig is the only structured preference accepted by the settings contract.
// APIKey is write-only at the HTTP boundary and ciphertext in persisted JSON.
type AIConfig struct {
	UseCustom     bool   `json:"use_custom"`
	Service       string `json:"service,omitempty"`
	Provider      string `json:"provider"`
	Endpoint      string `json:"endpoint"`
	Model         string `json:"model"`
	APIKey        string `json:"api_key,omitempty"`
	ClearAPIKey   bool   `json:"clear_api_key,omitempty"`
	KeyConfigured bool   `json:"key_configured,omitempty"`
}

// ParseAI rejects unknown fields and unbounded or ambiguous provider targets.
func ParseAI(raw json.RawMessage) (AIConfig, error) {
	var cfg AIConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) == 0 || string(raw) == "null" || decoder.Decode(&cfg) != nil {
		return cfg, ErrPreferences
	}
	if cfg.Service != "" && cfg.Service != "enterprise" && cfg.Service != "personal" && cfg.Service != "platform" {
		return cfg, ErrPreferences
	}
	switch {
	case cfg.Service == "personal":
		cfg.UseCustom = true
	case cfg.Service == "enterprise" || cfg.Service == "platform":
		cfg.UseCustom = false
	case cfg.UseCustom:
		cfg.Service = "personal"
	default:
		cfg.Service = "enterprise"
	}
	if cfg.Provider == "" {
		cfg.Provider = "openai"
	}
	if cfg.Provider != "openai" && cfg.Provider != "azure" && cfg.Provider != "ollama" {
		return cfg, ErrPreferences
	}
	for _, value := range []string{cfg.Endpoint, cfg.Model, cfg.APIKey} {
		if strings.ContainsFunc(value, unicode.IsControl) {
			return cfg, ErrPreferences
		}
	}
	keyLimit := 4096
	if strings.HasPrefix(cfg.APIKey, "enc:v1:") {
		keyLimit = 6144
	}
	if len(cfg.Endpoint) > 2048 || len(cfg.Model) > 256 || len(cfg.APIKey) > keyLimit || cfg.ClearAPIKey && cfg.APIKey != "" {
		return cfg, ErrPreferences
	}
	if cfg.Endpoint != "" {
		u, err := url.Parse(cfg.Endpoint)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
			(u.Scheme != "https" && (cfg.Provider != "ollama" || u.Scheme != "http")) {
			return cfg, ErrPreferences
		}
	}
	if cfg.UseCustom && (cfg.Endpoint == "" || strings.TrimSpace(cfg.Model) == "") {
		return cfg, ErrPreferences
	}
	return cfg, nil
}

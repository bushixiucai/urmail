package settings_test

import (
	"encoding/json"
	"testing"

	"github.com/bushixiucai/urmail/internal/domain/settings"
)

func TestMerge3Layers(t *testing.T) {
	entries := []settings.SettingEntry{
		{
			Scope:   settings.ScopePlatform,
			ScopeID: 0,
			Key:     "smtp",
			Value:   json.RawMessage(`{"max_msg_size": 10485760, "banner": "urMail Platform", "features": {"tls": true, "sieve": true}}`),
		},
		{
			Scope:   settings.ScopeBrand,
			ScopeID: 10,
			Key:     "smtp",
			Value:   json.RawMessage(`{"banner": "MyBrand Mail", "features": {"sieve": false}}`),
		},
		{
			Scope:   settings.ScopeTenant,
			ScopeID: 100,
			Key:     "smtp",
			Value:   json.RawMessage(`{"max_msg_size": 20971520}`),
		},
		{
			Scope:   settings.ScopePlatform,
			ScopeID: 0,
			Key:     "retention_days",
			Value:   json.RawMessage(`180`),
		},
		{
			Scope:   settings.ScopeTenant,
			ScopeID: 100,
			Key:     "retention_days",
			Value:   json.RawMessage(`365`),
		},
	}

	merged, err := settings.Merge3Layers(entries)
	if err != nil {
		t.Fatalf("Merge3Layers failed: %v", err)
	}

	// 1. Verify scalar override: retention_days -> 365
	if string(merged["retention_days"]) != "365" {
		t.Errorf("expected retention_days 365, got %s", merged["retention_days"])
	}

	// 2. Verify deep map merge
	var smtp struct {
		MaxMsgSize int64  `json:"max_msg_size"`
		Banner     string `json:"banner"`
		Features   struct {
			TLS   bool `json:"tls"`
			Sieve bool `json:"sieve"`
		} `json:"features"`
	}
	if err := json.Unmarshal(merged["smtp"], &smtp); err != nil {
		t.Fatalf("unmarshal merged smtp: %v", err)
	}

	// Tenant overrides max_msg_size to 20971520
	if smtp.MaxMsgSize != 20971520 {
		t.Errorf("expected MaxMsgSize 20971520, got %d", smtp.MaxMsgSize)
	}
	// Brand overrides banner to "MyBrand Mail"
	if smtp.Banner != "MyBrand Mail" {
		t.Errorf("expected Banner 'MyBrand Mail', got %q", smtp.Banner)
	}
	// Platform provides features.tls = true (inherited through brand and tenant)
	if !smtp.Features.TLS {
		t.Errorf("expected Features.TLS true, got false")
	}
	// Brand overrides features.sieve to false (tenant didn't touch it)
	if smtp.Features.Sieve {
		t.Errorf("expected Features.Sieve false, got true")
	}
}

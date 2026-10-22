package compliance_test

import (
	"encoding/json"
	"testing"

	"github.com/bushixiucai/urmail/internal/domain/compliance"
)

func TestMaskAuditDetail(t *testing.T) {
	raw := map[string]any{
		"action":        "user.create",
		"username":      "alice",
		"password":      "SuperSecretP@ss",
		"private_key":   "-----BEGIN RSA PRIVATE KEY-----...",
		"metadata": map[string]any{
			"api_key": "sk-123456",
			"note":    "test note",
		},
	}

	maskedBytes, err := compliance.MaskAuditDetail(raw)
	if err != nil {
		t.Fatalf("MaskAuditDetail failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(maskedBytes, &parsed); err != nil {
		t.Fatalf("unmarshal masked: %v", err)
	}

	if parsed["password"] != "[REDACTED]" {
		t.Errorf("expected password redacted, got %v", parsed["password"])
	}
	if parsed["private_key"] != "[REDACTED]" {
		t.Errorf("expected private_key redacted, got %v", parsed["private_key"])
	}

	meta := parsed["metadata"].(map[string]any)
	if meta["api_key"] != "[REDACTED]" {
		t.Errorf("expected nested api_key redacted, got %v", meta["api_key"])
	}
	if meta["note"] != "test note" {
		t.Errorf("expected non-sensitive note preserved, got %v", meta["note"])
	}
}

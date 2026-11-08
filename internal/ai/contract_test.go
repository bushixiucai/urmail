package ai

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestStructuredRequestUsesV2Templates pins the contract-PR switch: the
// assist boundary assembles the structured generation (version 2) and keeps
// the BuildOperation bounds contract.
func TestStructuredRequestUsesV2Templates(t *testing.T) {
	req, err := StructuredRequest(OperationInput{Operation: "classify", Body: "b", Subject: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if !req.JSONMode || len(req.Messages) != 2 {
		t.Fatalf("request shape: %+v", req)
	}
	if !strings.Contains(req.Messages[0].Content, "confidence") {
		t.Fatalf("structured schema not used: %s", req.Messages[0].Content)
	}
	if req.MaxTokens == nil || *req.MaxTokens != 2048 {
		t.Fatalf("max tokens: %v", req.MaxTokens)
	}

	if _, err := StructuredRequest(OperationInput{Operation: "nope", Body: "b"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unknown op: %v", err)
	}
	if _, err := StructuredRequest(OperationInput{Operation: "translate", Body: "b"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing target: %v", err)
	}
	if _, err := StructuredRequest(OperationInput{Operation: "summarize", Body: ""}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty body: %v", err)
	}
	if v := StructuredTemplateVersion("summarize"); v != 2 {
		t.Fatalf("template version: %d", v)
	}
	if v := StructuredTemplateVersion("nope"); v != 0 {
		t.Fatalf("unknown version: %d", v)
	}
}

// TestValidatePayloadLegacyAliases pins the one-release compatibility
// aliases: legacy frontends keep rendering while new fields are
// authoritative.
func TestValidatePayloadLegacyAliases(t *testing.T) {
	out, err := ValidatePayload("summarize", `{"bullets":["a","b"],"todos":[],"key_dates":[],"language":"en"}`)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["summary"] != "a\nb" {
		t.Fatalf("summary alias: %v", m["summary"])
	}

	out, err = ValidatePayload("polish", `{"revised_text":"better","changes":[],"tone":"formal"}`)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(out, &m)
	if m["draft"] != "better" {
		t.Fatalf("draft alias: %v", m["draft"])
	}

	out, err = ValidatePayload("translate", `{"translated_text":"hola","detected_source":"en"}`)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(out, &m)
	if m["translation"] != "hola" {
		t.Fatalf("translation alias: %v", m["translation"])
	}
}

package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustValidate(t *testing.T, op, raw string) map[string]any {
	t.Helper()
	out, err := ValidatePayload(op, raw)
	if err != nil {
		t.Fatalf("validate %s: %v", op, err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("normalized %s not json: %v", op, err)
	}
	return m
}

func mustFail(t *testing.T, op, raw, detail string) {
	t.Helper()
	if _, err := ValidatePayload(op, raw); err == nil {
		t.Fatalf("%s accepted invalid payload (%s): %s", op, detail, raw)
	}
}

// TestValidatePayloadClassify pins the confidence/score compatibility rule.
func TestValidatePayloadClassify(t *testing.T) {
	out := mustValidate(t, "classify", `{"category":"finance","confidence":0.87}`)
	if out["category"] != "finance" || out["confidence"] != 0.87 || out["score"] != 0.87 {
		t.Fatalf("classify normalization: %v", out)
	}
	// Legacy-style score-only output is accepted and echoed as confidence.
	out = mustValidate(t, "classify", `{"category":"spam","score":0.4}`)
	if out["confidence"] != 0.4 || out["score"] != 0.4 {
		t.Fatalf("score fallback: %v", out)
	}
	// Scores clamp into [0,1].
	out = mustValidate(t, "classify", `{"category":"spam","confidence":1.7}`)
	if out["confidence"] != 1.0 {
		t.Fatalf("clamp: %v", out)
	}
	mustFail(t, "classify", `{"category":"not_a_category","confidence":0.5}`, "enum")
	mustFail(t, "classify", `{"category":"spam"}`, "missing score")
	mustFail(t, "classify", `{"category":"spam","confidence":0.5,"extra":1}`, "unknown field")
}

// TestValidatePayloadSummarize covers the structured summary shape.
func TestValidatePayloadSummarize(t *testing.T) {
	out := mustValidate(t, "summarize", `{"bullets":["Trial ends Nov 15"],"todos":[{"task":"upgrade","assignee":null,"due_date":"2026-11-15"}],"key_dates":[{"date":"2026-11-15","label":"trial ends"}],"language":"zh-CN"}`)
	if out["language"] != "zh-CN" {
		t.Fatalf("language: %v", out)
	}
	todos := out["todos"].([]any)
	if len(todos) != 1 {
		t.Fatalf("todos: %v", out)
	}
	// Missing language defaults to unknown; null lists normalize to [].
	out = mustValidate(t, "summarize", `{"bullets":["one"],"todos":null,"key_dates":null}`)
	if out["language"] != "unknown" {
		t.Fatalf("language default: %v", out)
	}
	if len(out["todos"].([]any)) != 0 {
		t.Fatalf("todos null normalization: %v", out)
	}
	mustFail(t, "summarize", `{"bullets":[]}`, "empty bullets")
	mustFail(t, "summarize", `{"bullets":["a","b","c","d","e","f","g"]}`, "too many bullets")
	mustFail(t, "summarize", `{"bullets":["   "]}`, "blank bullet")
	mustFail(t, "summarize", `{"bullets":["a"],"unknown":1}`, "unknown field")
}

// TestValidatePayloadDraftAndPolish covers draft and polish shapes.
func TestValidatePayloadDraftAndPolish(t *testing.T) {
	out := mustValidate(t, "draft_reply", `{"subject":"Re: trial","body":"Body text","tone_notes":"polite decline","assumptions":["assuming they want to cancel"]}`)
	if out["subject"] != "Re: trial" || len(out["assumptions"].([]any)) != 1 {
		t.Fatalf("draft normalization: %v", out)
	}
	mustFail(t, "draft_reply", `{"subject":"two\nlines","body":"b"}`, "newline in subject")
	mustFail(t, "draft_new", `{"subject":"s","body":"  "}`, "blank body")
	mustFail(t, "draft_new", `{"subject":"s","body":"b","assumptions":["1","2","3","4","5","6"]}`, "too many assumptions")

	out = mustValidate(t, "polish", `{"revised_text":"better text","changes":[{"type":"clarity","note":"split run-on"}],"tone":""}`)
	if out["tone"] != "neutral" {
		t.Fatalf("tone default: %v", out)
	}
	mustFail(t, "polish", `{"revised_text":"x","changes":[{"type":"vibes","note":"n"}]}`, "change enum")
	mustFail(t, "polish", `{"revised_text":"x","tone":"aggressive"}`, "tone enum")
}

// TestValidatePayloadTranslateAndPhishing covers translate and the
// score-mapped phishing risk level.
func TestValidatePayloadTranslateAndPhishing(t *testing.T) {
	out := mustValidate(t, "translate", `{"translated_text":"こんにちは","detected_source":"zh-CN"}`)
	if out["detected_source"] != "zh-CN" {
		t.Fatalf("translate: %v", out)
	}
	mustFail(t, "translate", `{"translated_text":""}`, "empty translation")

	// risk_level is always derived from the score: the model's value is
	// never trusted over the evidence.
	out = mustValidate(t, "phishing", `{"score":0.95,"risk_level":"low","reasons":["spoofed domain"],"indicators":[{"type":"spoofed_domain","evidence":"paypa1.example"}]}`)
	if out["risk_level"] != "critical" {
		t.Fatalf("risk_level not score-mapped: %v", out)
	}
	out = mustValidate(t, "phishing", `{"score":0.1,"risk_level":"critical","reasons":[],"indicators":null}`)
	if out["risk_level"] != "low" {
		t.Fatalf("low score mapping: %v", out)
	}
	if len(out["indicators"].([]any)) != 0 {
		t.Fatalf("indicators normalization: %v", out)
	}
	mustFail(t, "phishing", `{"score":0.5,"risk_level":"low","reasons":["r"],"indicators":[{"type":"mind_control","evidence":"e"}]}`, "indicator enum")
	mustFail(t, "phishing", `{"risk_level":"high"}`, "missing score")
}

// TestValidatePayloadTodosAndCustom keeps the legacy shapes bounded.
func TestValidatePayloadTodosAndCustom(t *testing.T) {
	out := mustValidate(t, "todos", `{"items":[{"task":"send invoice","assignee":null,"due_date":null}]}`)
	if len(out["items"].([]any)) != 1 {
		t.Fatalf("todos: %v", out)
	}
	mustFail(t, "todos", `{}`, "missing items")
	mustFail(t, "todos", `{"items":[{"task":"  "}]}`, "blank task")

	out = mustValidate(t, "custom", `{"task_type":"explanation","result":"it explains"}`)
	if out["task_type"] != "explanation" {
		t.Fatalf("custom: %v", out)
	}
	mustFail(t, "custom", `{"task_type":"hacking","result":"r"}`, "task_type enum")
}

// TestValidatePayloadBoundaries rejects structural garbage.
func TestValidatePayloadBoundaries(t *testing.T) {
	if _, err := ValidatePayload("classify", ""); err == nil {
		t.Fatal("empty accepted")
	}
	if _, err := ValidatePayload("classify", strings.Repeat("x", 70000)); err == nil {
		t.Fatal("oversize accepted")
	}
	if _, err := ValidatePayload("classify", `{"category":"spam","confidence":0.5} trailing`); err == nil {
		t.Fatal("trailing content accepted")
	}
	if _, err := ValidatePayload("nope", `{}`); err == nil {
		t.Fatal("unknown operation accepted")
	}
}

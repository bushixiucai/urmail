package ai_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/ai"
	"github.com/bushixiucai/urmail/internal/ai/templates"
)

// TestPipelineHappyPath runs one structured operation end to end against a
// mock provider and locks the Result contract.
func TestPipelineHappyPath(t *testing.T) {
	mock := &captureProvider{
		completeFn: func(_ context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
			return ai.CompletionResponse{Content: "```json\n{\"category\":\"finance\",\"confidence\":0.91}\n```"}, nil
		},
	}
	p := ai.NewPipeline(mock, ai.WithPipelineModel("test-model"))

	res, err := p.Execute(context.Background(), "classify", templates.Params{Subject: "invoice", Body: "your invoice is ready"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Operation != "classify" || res.Degraded {
		t.Fatalf("result meta: %+v", res)
	}
	if res.TemplateVersion != 2 {
		t.Fatalf("structured generation version: %d", res.TemplateVersion)
	}
	var payload map[string]any
	if err := json.Unmarshal(res.Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload["category"] != "finance" || payload["score"] != 0.91 {
		t.Fatalf("payload normalization: %v", payload)
	}
	// The request went to the v2 template with the isolation envelope.
	u := mock.last.Messages[1].Content
	if !strings.Contains(u, "<email_content:") || !strings.Contains(mock.last.Messages[0].Content, "confidence") {
		t.Fatalf("structured template not used: %s", u)
	}
}

// TestPipelineValidationFailure and friends pin the error paths that the
// retry/degrade stage will hook into.
func TestPipelineErrorPaths(t *testing.T) {
	t.Run("schema violation", func(t *testing.T) {
		mock := &captureProvider{
			completeFn: func(_ context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
				return ai.CompletionResponse{Content: `{"summary":"legacy shape"}`}, nil
			},
		}
		p := ai.NewPipeline(mock)
		if _, err := p.Execute(context.Background(), "summarize", templates.Params{Body: "b"}); err == nil {
			t.Fatal("legacy shape accepted by structured validation")
		}
	})
	t.Run("unknown operation", func(t *testing.T) {
		p := ai.NewPipeline(&captureProvider{})
		if _, err := p.Execute(context.Background(), "sleep", templates.Params{Body: "b"}); err == nil {
			t.Fatal("unknown pipeline operation accepted")
		}
	})
	t.Run("missing parameter", func(t *testing.T) {
		p := ai.NewPipeline(&captureProvider{})
		if _, err := p.Execute(context.Background(), "translate", templates.Params{Body: "b"}); err == nil {
			t.Fatal("translate without target accepted")
		}
	})
	t.Run("provider error", func(t *testing.T) {
		mock := &captureProvider{
			completeFn: func(_ context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
				return ai.CompletionResponse{}, ai.ErrProviderError
			},
		}
		p := ai.NewPipeline(mock)
		if _, err := p.Execute(context.Background(), "classify", templates.Params{Body: "b"}); err == nil {
			t.Fatal("provider error swallowed")
		}
	})
}

// TestPipelineSummarizeStructured locks the summarize v2 payload shape
// through the pipeline.
func TestPipelineSummarizeStructured(t *testing.T) {
	mock := &captureProvider{
		completeFn: func(_ context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
			return ai.CompletionResponse{Content: `{"bullets":["Trial ends Nov 15","History locks after"],"todos":[],"key_dates":[{"date":"2026-11-15","label":"trial ends"}],"language":"en"}`}, nil
		},
	}
	p := ai.NewPipeline(mock)
	res, err := p.Execute(context.Background(), "summarize", templates.Params{Body: "trial text"})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Bullets  []string `json:"bullets"`
		KeyDates []struct {
			Date  string `json:"date"`
			Label string `json:"label"`
		} `json:"key_dates"`
		Language string `json:"language"`
	}
	if err := json.Unmarshal(res.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Bullets) != 2 || payload.Language != "en" || len(payload.KeyDates) != 1 {
		t.Fatalf("summarize payload: %+v", payload)
	}
}

package ai_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/ai"
)

// captureProvider records the last request so tests can assert the prompt
// assembly contract.
type captureProvider struct {
	completeFn func(ctx context.Context, req ai.CompletionRequest) (ai.CompletionResponse, error)
	last       ai.CompletionRequest
}

func (c *captureProvider) Name() string { return "capture" }

func (c *captureProvider) Complete(ctx context.Context, req ai.CompletionRequest) (ai.CompletionResponse, error) {
	c.last = req
	if c.completeFn != nil {
		return c.completeFn(ctx, req)
	}
	return ai.CompletionResponse{Content: "{}"}, nil
}

// TestAssistantPromptsUseIsolationEnvelope locks the migration contract:
// Capabilities methods send registry-built requests whose user message
// carries the nonce-wrapped untrusted content and whose system message
// keeps the operation instructions.
func TestAssistantPromptsUseIsolationEnvelope(t *testing.T) {
	cap := &captureProvider{
		completeFn: func(_ context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
			return ai.CompletionResponse{Content: `{"category": "work", "score": 0.5}`}, nil
		},
	}
	a := ai.NewAssistant(cap)

	if _, _, err := a.ClassifyEmailWithContext(context.Background(), "Trial ends", "pay now"); err != nil {
		t.Fatal(err)
	}
	req := cap.last
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
		t.Fatalf("message layout: %+v", req.Messages)
	}
	if !req.JSONMode {
		t.Fatal("classify lost json mode")
	}
	u := req.Messages[1].Content
	if !strings.Contains(u, "<email_content:") || !strings.Contains(u, "</email_content:") {
		t.Fatalf("isolation envelope missing: %s", u)
	}
	if !strings.Contains(u, "pay now") {
		t.Fatalf("body content lost: %s", u)
	}
	if !strings.Contains(u, "DATA to analyze or transform") {
		t.Fatalf("data declaration missing: %s", u)
	}

	// The nonce is per request: two calls never share an envelope.
	if _, _, err := a.ClassifyEmailWithContext(context.Background(), "Trial ends", "pay now"); err != nil {
		t.Fatal(err)
	}
	if cap.last.Messages[1].Content == u {
		t.Fatal("envelope nonce repeated across requests")
	}
}

// TestAssistantPhishingKeepsContract pins the phishing path end to end:
// wrapped content in, parsed/clamped payload out.
func TestAssistantPhishingKeepsContract(t *testing.T) {
	mock := &captureProvider{
		completeFn: func(_ context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
			return ai.CompletionResponse{Content: `{"phishing_score": 1.7, "reasons": ["spoofed domain"]}`}, nil
		},
	}
	a := ai.NewAssistant(mock)

	score, reasons, err := a.DetectPhishingWithContext(context.Background(), "a@x", "urgent", "click here", []string{"https://a"})
	if err != nil {
		t.Fatal(err)
	}
	if score != 1.0 {
		t.Fatalf("score not clamped: %v", score)
	}
	if len(reasons) != 1 || reasons[0] != "spoofed domain" {
		t.Fatalf("reasons: %v", reasons)
	}
	u := mock.last.Messages[1].Content
	for _, want := range []string{"From: a@x", "Links:\n- https://a", "<email_content:"} {
		if !strings.Contains(u, want) {
			t.Fatalf("phishing envelope missing %q: %s", want, u)
		}
	}
}

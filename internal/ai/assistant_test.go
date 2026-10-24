package ai_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/ai"
)

type mockProvider struct {
	name       string
	completeFn func(ctx context.Context, req ai.CompletionRequest) (ai.CompletionResponse, error)
}

func (m *mockProvider) Name() string {
	if m.name != "" {
		return m.name
	}
	return "mock"
}

func (m *mockProvider) Complete(ctx context.Context, req ai.CompletionRequest) (ai.CompletionResponse, error) {
	if m.completeFn != nil {
		return m.completeFn(ctx, req)
	}
	return ai.CompletionResponse{Content: "mock reply"}, nil
}

func TestAssistant_ClassifyEmail(t *testing.T) {
	t.Run("successful classification", func(t *testing.T) {
		mock := &mockProvider{
			completeFn: func(_ context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
				return ai.CompletionResponse{
					Content: `{"category": "work", "score": 0.95}`,
				}, nil
			},
		}
		assistant := ai.NewEmailAssistant(mock, ai.WithModel("test-model"), ai.WithTimeout(5*time.Second))
		category, score, err := assistant.ClassifyEmail("Project Roadmap Q3", "Let us review milestones.")
		if err != nil {
			t.Fatalf("ClassifyEmail failed: %v", err)
		}
		if category != "work" {
			t.Errorf("expected work, got %s", category)
		}
		if score != 0.95 {
			t.Errorf("expected score 0.95, got %f", score)
		}
	})

	t.Run("classification with markdown code block", func(t *testing.T) {
		mock := &mockProvider{
			completeFn: func(_ context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
				return ai.CompletionResponse{
					Content: "```json\n{\"category\": \"finance\", \"score\": 0.88}\n```",
				}, nil
			},
		}
		assistant := ai.NewAssistant(mock)
		category, score, err := assistant.ClassifyEmail("Invoice #1024", "Please find attached receipt.")
		if err != nil {
			t.Fatalf("ClassifyEmail failed: %v", err)
		}
		if category != "finance" || score != 0.88 {
			t.Errorf("expected finance/0.88, got %s/%f", category, score)
		}
	})

	t.Run("empty subject and body", func(t *testing.T) {
		mock := &mockProvider{}
		assistant := ai.NewAssistant(mock)
		category, score, err := assistant.ClassifyEmail("", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if category != "unknown" || score != 0.0 {
			t.Errorf("expected unknown/0.0, got %s/%f", category, score)
		}
	})

	t.Run("provider failure", func(t *testing.T) {
		mock := &mockProvider{
			completeFn: func(_ context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
				return ai.CompletionResponse{}, errors.New("network down")
			},
		}
		assistant := ai.NewAssistant(mock)
		_, _, err := assistant.ClassifyEmail("Subject", "Body")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestAssistant_SummarizeEmail(t *testing.T) {
	t.Run("successful summary with max length truncation", func(t *testing.T) {
		mock := &mockProvider{
			completeFn: func(_ context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
				return ai.CompletionResponse{
					Content: "This is a long summary exceeding the maximum character length allowed.",
				}, nil
			},
		}
		assistant := ai.NewAssistant(mock)
		summary, err := assistant.SummarizeEmail("Long email body...", 20)
		if err != nil {
			t.Fatalf("SummarizeEmail failed: %v", err)
		}
		if len([]rune(summary)) != 20 {
			t.Errorf("expected length 20, got %d (%q)", len([]rune(summary)), summary)
		}
	})

	t.Run("empty body returns empty", func(t *testing.T) {
		mock := &mockProvider{}
		assistant := ai.NewAssistant(mock)
		summary, err := assistant.SummarizeEmail("   ", 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if summary != "" {
			t.Errorf("expected empty string, got %q", summary)
		}
	})
}

func TestAssistant_TranslateEmail(t *testing.T) {
	t.Run("successful translation", func(t *testing.T) {
		mock := &mockProvider{
			completeFn: func(_ context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
				return ai.CompletionResponse{
					Content: "Bonjour le monde",
				}, nil
			},
		}
		assistant := ai.NewAssistant(mock)
		trans, err := assistant.TranslateEmail("Hello world", "French")
		if err != nil {
			t.Fatalf("TranslateEmail failed: %v", err)
		}
		if trans != "Bonjour le monde" {
			t.Errorf("expected translation Bonjour le monde, got %s", trans)
		}
	})

	t.Run("missing target language", func(t *testing.T) {
		mock := &mockProvider{}
		assistant := ai.NewAssistant(mock)
		_, err := assistant.TranslateEmail("Hello", "")
		if err == nil {
			t.Fatal("expected error for empty target language")
		}
	})

	t.Run("empty body", func(t *testing.T) {
		mock := &mockProvider{}
		assistant := ai.NewAssistant(mock)
		trans, err := assistant.TranslateEmail("", "Spanish")
		if err != nil || trans != "" {
			t.Errorf("expected empty translation and nil err, got %q, %v", trans, err)
		}
	})
}

func TestAssistant_DetectPhishing(t *testing.T) {
	t.Run("phishing detection alert", func(t *testing.T) {
		mock := &mockProvider{
			completeFn: func(_ context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
				return ai.CompletionResponse{
					Content: `{"phishing_score": 0.95, "reasons": ["spoofed domain", "urgent password reset demand"]}`,
				}, nil
			},
		}
		assistant := ai.NewAssistant(mock)
		score, reasons, err := assistant.DetectPhishing(
			"security@paypa1-update.com",
			"URGENT: Verify your account immediately",
			"Click here to enter your credentials or account will be suspended.",
			[]string{"http://192.168.1.100/login", "http://paypa1-update.com/verify"},
		)
		if err != nil {
			t.Fatalf("DetectPhishing failed: %v", err)
		}
		if score < 0.9 {
			t.Errorf("expected high phishing score, got %f", score)
		}
		if len(reasons) != 2 {
			t.Errorf("expected 2 reasons, got %d", len(reasons))
		}
	})

	t.Run("safe email", func(t *testing.T) {
		mock := &mockProvider{
			completeFn: func(_ context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
				return ai.CompletionResponse{
					Content: `{"phishing_score": 0.05, "reasons": []}`,
				}, nil
			},
		}
		assistant := ai.NewAssistant(mock)
		score, reasons, err := assistant.DetectPhishing(
			"team@company.internal",
			"Weekly Sync Agenda",
			"Here is the agenda for tomorrow meeting.",
			nil,
		)
		if err != nil {
			t.Fatalf("DetectPhishing failed: %v", err)
		}
		if score != 0.05 {
			t.Errorf("expected score 0.05, got %f", score)
		}
		if len(reasons) != 0 {
			t.Errorf("expected 0 reasons, got %d", len(reasons))
		}
	})

	t.Run("invalid json response from provider", func(t *testing.T) {
		mock := &mockProvider{
			completeFn: func(_ context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
				return ai.CompletionResponse{Content: "not json at all"}, nil
			},
		}
		assistant := ai.NewAssistant(mock)
		_, _, err := assistant.DetectPhishing("a@b.com", "sub", "body", nil)
		if err == nil || !strings.Contains(err.Error(), "parse phishing json") {
			t.Errorf("expected parse error, got %v", err)
		}
	})
}
func TestAssistant_ClassifyEmail_ZeroScorePreserved(t *testing.T) {
	mock := &mockProvider{
		completeFn: func(_ context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
			return ai.CompletionResponse{Content: `{"category": "work", "score": 0}`}, nil
		},
	}
	assistant := ai.NewAssistant(mock)
	category, score, err := assistant.ClassifyEmail("hello", "world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if category != "work" {
		t.Errorf("expected work, got %s", category)
	}
	if score != 0.0 {
		t.Errorf("score 0 must be preserved as a legal value, got %f", score)
	}
}

func TestAssistant_ContextTimeoutDistinguishedFromProviderError(t *testing.T) {
	t.Run("timeout classified as context deadline", func(t *testing.T) {
		mock := &mockProvider{
			completeFn: func(ctx context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
				select {
				case <-ctx.Done():
					return ai.CompletionResponse{}, ctx.Err()
				case <-time.After(300 * time.Millisecond):
					return ai.CompletionResponse{Content: "{}"}, nil
				}
			},
		}
		assistant := ai.NewAssistant(mock, ai.WithTimeout(30*time.Millisecond))
		_, _, err := assistant.ClassifyEmail("s", "b")
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("expected context.DeadlineExceeded, got %v", err)
		}
	})

	t.Run("cancellation classified as context canceled", func(t *testing.T) {
		mock := &mockProvider{
			completeFn: func(ctx context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
				<-ctx.Done()
				return ai.CompletionResponse{}, ctx.Err()
			},
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err := ai.NewAssistant(mock).ClassifyEmailWithContext(ctx, "s", "b")
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	})

	t.Run("provider error not misreported as timeout", func(t *testing.T) {
		mock := &mockProvider{
			completeFn: func(_ context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
				return ai.CompletionResponse{}, ai.ErrProviderError
			},
		}
		_, _, err := ai.NewAssistant(mock, ai.WithTimeout(5*time.Second)).ClassifyEmail("s", "b")
		if !errors.Is(err, ai.ErrProviderError) {
			t.Errorf("expected ErrProviderError, got %v", err)
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			t.Errorf("provider error must not be classified as context error: %v", err)
		}
	})
}

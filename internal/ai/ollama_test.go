package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bushixiucai/urmail/internal/ai"
)

func TestOllamaProvider_P06_LocalOnlyEnforcement(t *testing.T) {
	disallowedURLs := []string{
		"http://api.openai.com",
		"https://api.ollama.com",
		"http://192.168.1.1:11434",
		"http://10.0.0.5:11434",
		"http://172.16.0.1:11434",
		"http://8.8.8.8:11434",
		"http://evil.hacker.com:11434",
	}

	for _, badURL := range disallowedURLs {
		t.Run("reject_"+badURL, func(t *testing.T) {
			_, err := ai.NewOllamaProvider(ai.WithOllamaBaseURL(badURL))
			if err == nil {
				t.Fatalf("expected error for non-local url %s, got nil", badURL)
			}
			if !errors.Is(err, ai.ErrLocalOnly) {
				t.Errorf("expected ErrLocalOnly for %s, got %v", badURL, err)
			}
		})
	}

	allowedURLs := []string{
		"http://127.0.0.1:11434",
		"http://localhost:11434",
		"http://[::1]:11434",
		"http://127.0.0.2:11434",
	}

	for _, okURL := range allowedURLs {
		t.Run("allow_"+okURL, func(t *testing.T) {
			p, err := ai.NewOllamaProvider(ai.WithOllamaBaseURL(okURL))
			if err != nil {
				t.Fatalf("unexpected error for loopback url %s: %v", okURL, err)
			}
			if p.Name() != "ollama" {
				t.Errorf("expected name ollama, got %s", p.Name())
			}
		})
	}
}

func TestOllamaProvider_Complete_Native(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/chat" {
			t.Errorf("expected /api/chat, got %s", r.URL.Path)
		}

		var reqBody struct {
			Model    string                 `json:"model"`
			Messages []ai.Message           `json:"messages"`
			Stream   bool                   `json:"stream"`
			Format   string                 `json:"format"`
			Options  map[string]interface{} `json:"options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Errorf("failed to decode json body: %v", err)
		}
		if reqBody.Stream {
			t.Error("stream must be false")
		}
		if reqBody.Model != "qwen2.5" {
			t.Errorf("expected model qwen2.5, got %s", reqBody.Model)
		}
		if reqBody.Format != "json" {
			t.Errorf("expected format json, got %s", reqBody.Format)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{\"model\":\"qwen2.5\",\"message\":{\"role\":\"assistant\",\"content\":\"{\\\"status\\\":\\\"ok\\\"}\"},\"done\":true,\"done_reason\":\"stop\",\"prompt_eval_count\":20,\"eval_count\":15}"))
	}))
	defer srv.Close()

	p, err := ai.NewOllamaProvider(
		ai.WithOllamaBaseURL(srv.URL),
		ai.WithOllamaModel("llama3"),
	)
	if err != nil {
		t.Fatalf("NewOllamaProvider failed: %v", err)
	}

	temp := 0.2
	resp, err := p.Complete(context.Background(), ai.CompletionRequest{
		Model:       "qwen2.5",
		Prompt:      "generate json status",
		Temperature: &temp,
		JSONMode:    true,
	})
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}

	if resp.Content != `{"status":"ok"}` {
		t.Errorf("unexpected content: %s", resp.Content)
	}
	if resp.PromptTokens != 20 || resp.OutputTokens != 15 || resp.TotalTokens != 35 {
		t.Errorf("unexpected usage counts: %+v", resp)
	}
}

func TestOllamaProvider_Complete_OpenAICompat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("expected /v1/chat/completions, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{\"choices\":[{\"message\":{\"role\":\"assistant\",\"content\":\"compat answer\"}}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":5,\"total_tokens\":10}}"))
	}))
	defer srv.Close()

	p, err := ai.NewOllamaProvider(ai.WithOllamaBaseURL(srv.URL + "/v1"))
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	resp, err := p.Complete(context.Background(), ai.CompletionRequest{Prompt: "hi"})
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	if resp.Content != "compat answer" {
		t.Errorf("expected compat answer, got %s", resp.Content)
	}
}

func TestOllamaProvider_Errors(t *testing.T) {
	// Empty messages error
	p, err := ai.NewOllamaProvider()
	if err != nil {
		t.Fatalf("NewOllamaProvider failed: %v", err)
	}
	_, err = p.Complete(context.Background(), ai.CompletionRequest{})
	if !errors.Is(err, ai.ErrEmptyPrompt) {
		t.Errorf("expected ErrEmptyPrompt, got %v", err)
	}

	// Upstream error in json
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{\"error\": \"model not found\"}"))
	}))
	defer srv.Close()

	pErr, err := ai.NewOllamaProvider(ai.WithOllamaBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	_, err = pErr.Complete(context.Background(), ai.CompletionRequest{Prompt: "test"})
	if !errors.Is(err, ai.ErrProviderError) {
		t.Errorf("expected ErrProviderError, got %v", err)
	}
}

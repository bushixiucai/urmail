// Package ai defines LLM interfaces and provider adapters for email intelligence.
package ai

import (
	"context"
	"errors"
)

// Supported AI providers.
const (
	ProviderOpenAI = "openai"
	ProviderOllama = "ollama"
)

// Common AI errors.
var (
	ErrProviderDisabled  = errors.New("ai provider is disabled")
	ErrLocalOnlyEnforced = errors.New("external cloud calls prohibited under local-only policy")
	ErrLocalOnly         = ErrLocalOnlyEnforced // Alias for P06 rule enforcement
	ErrModelNotFound     = errors.New("specified ai model not found")
	ErrInvalidRequest    = errors.New("invalid ai request parameters")
	ErrEmptyPrompt       = errors.New("completion request contains no messages or prompt")
	ErrProviderError     = errors.New("ai provider returned an error")
	ErrRateLimitExceeded = errors.New("ai provider rate limit exceeded")
	ErrInvalidResponse   = errors.New("invalid or empty response from ai provider")
	ErrUnauthorized      = errors.New("unauthorized ai provider credentials")
)

// Message represents a single chat conversation message.
type Message struct {
	Role    string `json:"role"`    // "system", "user", "assistant"
	Content string `json:"content"` // message text
}

// CompletionRequest represents a generic prompt request.
type CompletionRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages,omitempty"`
	Prompt      string    `json:"prompt,omitempty"`
	System      string    `json:"system,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	JSONMode    bool      `json:"json_mode,omitempty"`
}

// EffectiveMessages returns a consolidated slice of chat messages.
func (r *CompletionRequest) EffectiveMessages() []Message {
	if len(r.Messages) > 0 {
		return r.Messages
	}
	var msgs []Message
	if r.System != "" {
		msgs = append(msgs, Message{Role: "system", Content: r.System})
	}
	if r.Prompt != "" {
		msgs = append(msgs, Message{Role: "user", Content: r.Prompt})
	}
	return msgs
}

// CompletionResponse holds the completion output.
type CompletionResponse struct {
	Content      string `json:"content"`
	Text         string `json:"text,omitempty"` // Alias for Content
	FinishReason string `json:"finish_reason,omitempty"`
	PromptTokens int    `json:"prompt_tokens"`
	OutputTokens int    `json:"output_tokens"`
	TotalTokens  int    `json:"total_tokens"`
}

// Provider defines the interface for an AI model backend.
type Provider interface {
	Name() string
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
}

// Capabilities defines email intelligence capabilities.
type Capabilities interface {
	ClassifyEmail(subject, body string) (category string, score float64, err error)
	SummarizeEmail(body string, maxLength int) (summary string, err error)
	TranslateEmail(body string, targetLang string) (translation string, err error)
	DetectPhishing(from, subject, body string, links []string) (phishingScore float64, reasons []string, err error)
}

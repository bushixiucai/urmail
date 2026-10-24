package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAssistantTimeout = 30 * time.Second
)

// Assistant implements the Capabilities interface using an underlying Provider.
type Assistant struct {
	provider Provider
	model    string
	timeout  time.Duration
}

// EmailAssistant is an alias for Assistant.
type EmailAssistant = Assistant

// AssistantOption configures an Assistant instance.
type AssistantOption func(*Assistant)

// WithModel sets the target model for the assistant.
func WithModel(model string) AssistantOption {
	return func(a *Assistant) {
		a.model = model
	}
}

// WithTimeout sets the operation timeout for the assistant.
func WithTimeout(d time.Duration) AssistantOption {
	return func(a *Assistant) {
		if d > 0 {
			a.timeout = d
		}
	}
}

// NewAssistant creates a new Assistant with the specified Provider.
func NewAssistant(provider Provider, opts ...AssistantOption) *Assistant {
	a := &Assistant{
		provider: provider,
		timeout:  defaultAssistantTimeout,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// NewEmailAssistant creates a new EmailAssistant instance.
func NewEmailAssistant(provider Provider, opts ...AssistantOption) *EmailAssistant {
	return NewAssistant(provider, opts...)
}

type classificationPayload struct {
	Category string  `json:"category"`
	Score    float64 `json:"score"`
}

// ClassifyEmail categorizes an email based on its subject and body.
func (a *Assistant) ClassifyEmail(subject, body string) (category string, score float64, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()
	return a.ClassifyEmailWithContext(ctx, subject, body)
}

// ClassifyEmailWithContext categorizes an email using the provided context.
func (a *Assistant) ClassifyEmailWithContext(ctx context.Context, subject, body string) (category string, score float64, err error) {
	if strings.TrimSpace(subject) == "" && strings.TrimSpace(body) == "" {
		return "unknown", 0.0, nil
	}

	systemPrompt := "You are an email classification assistant. Categorize the given email into exactly one of: " +
		"inbox, work, personal, marketing, finance, support, notifications, spam.\n" +
		"Respond ONLY with a JSON object in this format: {\"category\": \"<category>\", \"score\": <float between 0.0 and 1.0>}"

	userPrompt := fmt.Sprintf("Subject: %s\n\nBody:\n%s", subject, body)

	req := CompletionRequest{
		Model: a.model,
		Messages: []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		JSONMode: true,
	}

	resp, err := a.provider.Complete(ctx, req)
	if err != nil {
		return "", 0.0, wrapProviderError(ctx, err, "email classification")
	}

	cleaned := extractJSON(resp.Content)
	var result classificationPayload
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		return "", 0.0, fmt.Errorf("%w: failed to parse classification json: %w", ErrInvalidResponse, err)
	}

	category = strings.ToLower(strings.TrimSpace(result.Category))
	if category == "" {
		category = "unknown"
	}
	score = result.Score
	if score < 0.0 {
		score = 0.0
	} else if score > 1.0 {
		score = 1.0
	}
	return category, score, nil
}

// SummarizeEmail generates a concise summary of the email body.
func (a *Assistant) SummarizeEmail(body string, maxLength int) (summary string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()
	return a.SummarizeEmailWithContext(ctx, body, maxLength)
}

// SummarizeEmailWithContext generates a summary using the provided context.
func (a *Assistant) SummarizeEmailWithContext(ctx context.Context, body string, maxLength int) (summary string, err error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", nil
	}

	systemPrompt := "You are a concise email summarization assistant. Provide an objective, clear summary of the email. " +
		"Respond ONLY with the summary text, with no introductory phrases or metadata."
	if maxLength > 0 {
		systemPrompt += " The summary must not exceed " + strconv.Itoa(maxLength) + " characters."
	}

	req := CompletionRequest{
		Model: a.model,
		Messages: []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: body},
		},
	}

	resp, err := a.provider.Complete(ctx, req)
	if err != nil {
		return "", wrapProviderError(ctx, err, "email summarization")
	}

	summary = strings.TrimSpace(resp.Content)
	if maxLength > 0 {
		runes := []rune(summary)
		if len(runes) > maxLength {
			summary = string(runes[:maxLength])
		}
	}
	return summary, nil
}

// TranslateEmail translates the email body into the target language.
func (a *Assistant) TranslateEmail(body string, targetLang string) (translation string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()
	return a.TranslateEmailWithContext(ctx, body, targetLang)
}

// TranslateEmailWithContext translates the email body using the provided context.
func (a *Assistant) TranslateEmailWithContext(ctx context.Context, body string, targetLang string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", nil
	}
	targetLang = strings.TrimSpace(targetLang)
	if targetLang == "" {
		return "", errors.New("target language is required")
	}

	systemPrompt := fmt.Sprintf("You are a professional email translation assistant. "+
		"Translate the following email body into %s. Preserve paragraph structure and tone. "+
		"Respond ONLY with the translation, with no commentary.", targetLang)

	req := CompletionRequest{
		Model: a.model,
		Messages: []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: body},
		},
	}

	resp, err := a.provider.Complete(ctx, req)
	if err != nil {
		return "", wrapProviderError(ctx, err, "email translation")
	}
	return strings.TrimSpace(resp.Content), nil
}

type phishingPayload struct {
	PhishingScore float64  `json:"phishing_score"`
	Reasons       []string `json:"reasons"`
}

// DetectPhishing evaluates an email and extracted links for phishing threats.
func (a *Assistant) DetectPhishing(from, subject, body string, links []string) (phishingScore float64, reasons []string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()
	return a.DetectPhishingWithContext(ctx, from, subject, body, links)
}

// DetectPhishingWithContext evaluates phishing using the provided context.
func (a *Assistant) DetectPhishingWithContext(ctx context.Context, from, subject, body string, links []string) (float64, []string, error) {
	systemPrompt := "You are an expert cybersecurity email threat analyzer. " +
		"Evaluate the email for phishing indicators such as domain spoofing, urgent threats, credential harvesting, and deceptive links.\n" +
		"Respond ONLY with a JSON object in this format:\n" +
		"{\"phishing_score\": <float between 0.0 and 1.0>, \"reasons\": [\"<reason 1>\", \"<reason 2>\"]}\n" +
		"A score of 0.0 means completely safe; 1.0 means definite phishing threat."

	var linksFormatted string
	if len(links) > 0 {
		linksFormatted = "\nLinks:\n- " + strings.Join(links, "\n- ")
	} else {
		linksFormatted = "\nLinks: none"
	}

	userPrompt := fmt.Sprintf("From: %s\nSubject: %s%s\n\nBody:\n%s", from, subject, linksFormatted, body)

	req := CompletionRequest{
		Model: a.model,
		Messages: []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		JSONMode: true,
	}

	resp, err := a.provider.Complete(ctx, req)
	if err != nil {
		return 0.0, nil, wrapProviderError(ctx, err, "phishing detection")
	}

	cleaned := extractJSON(resp.Content)
	var result phishingPayload
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		return 0.0, nil, fmt.Errorf("%w: failed to parse phishing json: %w", ErrInvalidResponse, err)
	}

	score := result.PhishingScore
	if score < 0.0 {
		score = 0.0
	} else if score > 1.0 {
		score = 1.0
	}
	reasons := result.Reasons
	if reasons == nil {
		reasons = make([]string, 0)
	}
	return score, reasons, nil
}

// wrapProviderError distinguishes context deadline/cancellation from
// upstream provider failures: when the caller's context is already done the
// returned error wraps ctx.Err() (context.DeadlineExceeded or
// context.Canceled) so callers can classify timeouts without mistaking them
// for provider outages.
func wrapProviderError(ctx context.Context, err error, op string) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%s: %w: %w", op, ctxErr, err)
	}
	return fmt.Errorf("%s failed: %w", op, err)
}

// extractJSON extracts a JSON object from text, stripping markdown code block fences if present.
func extractJSON(input string) string {
	trimmed := strings.TrimSpace(input)
	// Handle ```json ... ``` blocks
	if idx := strings.Index(trimmed, "```json"); idx != -1 {
		rest := trimmed[idx+7:]
		if endIdx := strings.Index(rest, "```"); endIdx != -1 {
			return strings.TrimSpace(rest[:endIdx])
		}
	}
	// Handle generic ``` ... ``` blocks
	if idx := strings.Index(trimmed, "```"); idx != -1 {
		rest := trimmed[idx+3:]
		if endIdx := strings.Index(rest, "```"); endIdx != -1 {
			return strings.TrimSpace(rest[:endIdx])
		}
	}
	// Extract substring between first "{" and last "}"
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start != -1 && end != -1 && end > start {
		return trimmed[start : end+1]
	}
	return trimmed
}

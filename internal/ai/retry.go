package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/ai/templates"
)

// TECH §3 workflow: structured generation with one corrective retry and a
// plain-text fallback. Every attempt of one user operation shares the
// caller's billing reservation and context deadline; usage accumulates so
// the reservation settles once with the true token count.

// Degrade reasons carried by the response envelope.
const (
	DegradeProviderUnavailable  = "provider_unavailable"
	DegradeSchemaRetryExhausted = "schema_retry_exhausted"
)

// fallbackTemplateVersion is the template generation of the plain-text
// fallback prompts (internal fallback templates, not in the registry).
const fallbackTemplateVersion = 1

// DefaultCallTimeout bounds one user operation's provider rounds (all
// structured attempts plus the fallback resend share the deadline).
const DefaultCallTimeout = 30 * time.Second

// TranslateCallTimeout bounds a translate operation. Full-body
// translations are the longest structured output in the pipeline; on
// reasoning models they routinely exceed the default budget — QA DEF-B
// saw two translate 502s at ~30s while summarize on the same mail
// finished in 4.5s.
const TranslateCallTimeout = 90 * time.Second

// OperationCallTimeout returns the provider-call budget for one operation.
func OperationCallTimeout(operation string) time.Duration {
	if operation == "translate" {
		return TranslateCallTimeout
	}
	return DefaultCallTimeout
}

// StructuredUsage accumulates provider usage across the attempts of one
// operation. Known stays true only if every attempt reported usage.
type StructuredUsage struct {
	PromptTokens int
	OutputTokens int
	Known        bool
}

// StructuredParams maps an assist input onto template params (the same
// mapping StructuredRequest applies).
func StructuredParams(input OperationInput) templates.Params {
	return templates.Params{
		Draft:          input.Draft,
		Instruction:    input.Instruction,
		Subject:        input.Subject,
		Body:           input.Body,
		From:           input.From,
		TargetLanguage: input.TargetLanguage,
	}
}

// CompleteValidated runs one structured generation: provider call, JSON
// fence stripping, strict validation, one low-temperature corrective retry
// on schema failure and finally one plain-text fallback resend whose
// answer is served as result.text with degraded=true. Provider errors are
// never retried as structured calls — per the contract (CHANGELOG #65)
// they degrade directly through the same plain-text fallback resend, so
// the user gets a labeled usable answer even when JSON mode or long
// structured output is what the provider chokes on. Only when even the
// fallback resend cannot reach the provider does this return an error
// (the handler keeps its AI_UNAVAILABLE path for that hard case).
func CompleteValidated(ctx context.Context, provider Provider, operation string, req CompletionRequest, params templates.Params) (json.RawMessage, StructuredUsage, ResultMeta, error) {
	usage := StructuredUsage{Known: true}
	resp, err := provider.Complete(ctx, req)
	if err == nil {
		usage.add(resp)
		payload, verr := ValidatePayload(operation, ExtractJSON(resp.Content))
		if verr == nil {
			return payload, usage, ResultMeta{TemplateVersion: StructuredTemplateVersion(operation)}, nil
		}
		if errors.Is(verr, ErrInvalidResponse) {
			retry := req
			retry.Messages = append(append([]Message{}, req.Messages...), Message{
				Role:    "user",
				Content: fmt.Sprintf("Your previous output failed schema validation: %v.", verr) + " Return ONLY a JSON object matching the schema.",
			})
			zero := 0.0
			retry.Temperature = &zero
			resp, err = provider.Complete(ctx, retry)
			if err == nil {
				usage.add(resp)
				if payload, verr := ValidatePayload(operation, ExtractJSON(resp.Content)); verr == nil {
					return payload, usage, ResultMeta{TemplateVersion: StructuredTemplateVersion(operation)}, nil
				}
			}
		} else {
			// Request-shaped failures are bugs or bad input, not model noise.
			return nil, usage, ResultMeta{}, verr
		}
	}

	// Degradation path, reached on provider errors (unretried) and on
	// schema exhaustion alike. The reason records which one it was.
	degradeReason := DegradeSchemaRetryExhausted
	if err != nil {
		degradeReason = DegradeProviderUnavailable
	}
	treq, ferr := templates.BuildFallback(operation, params)
	if ferr != nil {
		return nil, usage, ResultMeta{}, ErrInvalidRequest
	}
	fallback := requestFromTemplate("", treq)
	fallback.Model = req.Model
	resp, ferr = provider.Complete(ctx, fallback)
	if ferr != nil {
		return nil, usage, ResultMeta{Degraded: true, DegradeReason: degradeReason}, ferr
	}
	usage.add(resp)
	text := strings.TrimSpace(resp.Content)
	if text == "" {
		return nil, usage, ResultMeta{Degraded: true, DegradeReason: degradeReason}, ErrInvalidResponse
	}
	payload, merr := json.Marshal(map[string]string{"text": text})
	if merr != nil {
		return nil, usage, ResultMeta{Degraded: true, DegradeReason: degradeReason}, ErrInvalidResponse
	}
	return payload, usage, ResultMeta{TemplateVersion: fallbackTemplateVersion, Degraded: true, DegradeReason: degradeReason}, nil
}

// add accumulates one provider response's usage; one unknown report makes
// the total unknown.
func (u *StructuredUsage) add(resp CompletionResponse) {
	if u.Known && resp.UsageKnown && resp.PromptTokens >= 0 && resp.OutputTokens >= 0 {
		u.PromptTokens += resp.PromptTokens
		u.OutputTokens += resp.OutputTokens
		return
	}
	u.Known = false
}

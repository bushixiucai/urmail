package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
// returned as errors (the handler keeps its AI_UNAVAILABLE path); schema
// exhaustion degrades instead of failing.
func CompleteValidated(ctx context.Context, provider Provider, operation string, req CompletionRequest, params templates.Params) (json.RawMessage, StructuredUsage, ResultMeta, error) {
	usage := StructuredUsage{Known: true}
	resp, err := provider.Complete(ctx, req)
	if err != nil {
		return nil, usage, ResultMeta{Degraded: true, DegradeReason: DegradeProviderUnavailable}, err
	}
	usage.add(resp)
	payload, verr := ValidatePayload(operation, ExtractJSON(resp.Content))
	if verr == nil {
		return payload, usage, ResultMeta{TemplateVersion: StructuredTemplateVersion(operation)}, nil
	}
	if !errors.Is(verr, ErrInvalidResponse) {
		// Request-shaped failures are bugs or bad input, not model noise.
		return nil, usage, ResultMeta{}, verr
	}

	retry := req
	retry.Messages = append(append([]Message{}, req.Messages...), Message{
		Role:    "user",
		Content: fmt.Sprintf("Your previous output failed schema validation: %v.", verr) + " Return ONLY a JSON object matching the schema.",
	})
	zero := 0.0
	retry.Temperature = &zero
	resp, err = provider.Complete(ctx, retry)
	if err != nil {
		return nil, usage, ResultMeta{Degraded: true, DegradeReason: DegradeProviderUnavailable}, err
	}
	usage.add(resp)
	if payload, verr := ValidatePayload(operation, ExtractJSON(resp.Content)); verr == nil {
		return payload, usage, ResultMeta{TemplateVersion: StructuredTemplateVersion(operation)}, nil
	}

	treq, ferr := templates.BuildFallback(operation, params)
	if ferr != nil {
		return nil, usage, ResultMeta{}, ErrInvalidRequest
	}
	fallback := requestFromTemplate("", treq)
	fallback.Model = req.Model
	resp, err = provider.Complete(ctx, fallback)
	if err != nil {
		return nil, usage, ResultMeta{Degraded: true, DegradeReason: DegradeProviderUnavailable}, err
	}
	usage.add(resp)
	text := strings.TrimSpace(resp.Content)
	if text == "" {
		return nil, usage, ResultMeta{Degraded: true, DegradeReason: DegradeSchemaRetryExhausted}, ErrInvalidResponse
	}
	payload, merr := json.Marshal(map[string]string{"text": text})
	if merr != nil {
		return nil, usage, ResultMeta{Degraded: true, DegradeReason: DegradeSchemaRetryExhausted}, ErrInvalidResponse
	}
	return payload, usage, ResultMeta{TemplateVersion: fallbackTemplateVersion, Degraded: true, DegradeReason: DegradeSchemaRetryExhausted}, nil
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

package ai

import (
	"github.com/bushixiucai/urmail/internal/ai/templates"
)

// Contract glue between the assist API surface and the structured pipeline
// generation (TECH §5). The request assembly mirrors BuildOperation's bounds
// and error contract, but renders the *_v2 templates validated by
// ValidatePayload. ResultMeta rides next to every result so the response
// envelope can attribute it to the exact prompt generation and degradation
// state; CacheHit is filled by the result-cache stage.

// ExtractJSON strips provider response decorations (markdown fences,
// surrounding prose) from a JSON object reply.
func ExtractJSON(content string) string {
	return extractJSON(content)
}

// StructuredRequest assembles the structured-generation request for one
// assist operation. Bounds and error mapping match BuildOperation.
func StructuredRequest(input OperationInput) (CompletionRequest, error) {
	if len(input.Draft) > 65536 || len(input.Instruction) > 4000 || len(input.Body) == 0 || len(input.Body) > 65536 || len(input.Subject) > 1000 || len(input.From) > 1000 || len(input.TargetLanguage) > 64 {
		return CompletionRequest{}, ErrInvalidRequest
	}
	params := templates.Params{
		Draft:          input.Draft,
		Instruction:    input.Instruction,
		Subject:        input.Subject,
		Body:           input.Body,
		From:           input.From,
		TargetLanguage: input.TargetLanguage,
		LengthLimit:    input.LengthLimit,
	}
	key, ok := structuredTemplates[input.Operation]
	if !ok {
		return CompletionRequest{}, ErrInvalidRequest
	}
	treq, err := templates.Build(key, params)
	if err != nil {
		// Unknown operations and missing parameters were always request
		// errors at this boundary.
		return CompletionRequest{}, ErrInvalidRequest
	}
	req := requestFromTemplate("", treq)
	if req.MaxTokens == nil {
		// Assist responses stay bounded even when a template leaves the
		// provider default in place.
		mt := 2048
		req.MaxTokens = &mt
	}
	return req, nil
}

// StructuredTemplateVersion reports the template generation an operation's
// structured request currently resolves to; 0 for unknown operations.
func StructuredTemplateVersion(operation string) int {
	key, ok := structuredTemplates[operation]
	if !ok {
		return 0
	}
	return templates.Version(key)
}

// ResultMeta describes how a result was produced.
type ResultMeta struct {
	// TemplateVersion is the prompt generation that produced the result;
	// 0 marks legacy or replayed rows predating the field.
	TemplateVersion int
	// Degraded marks a non-structured fallback answer.
	Degraded bool
	// DegradeReason is provider_unavailable | schema_retry_exhausted.
	DegradeReason string
	// CacheHit marks results served from the operation cache.
	CacheHit bool
}

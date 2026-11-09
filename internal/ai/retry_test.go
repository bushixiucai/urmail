package ai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type retryStub struct {
	name      string
	responses []CompletionResponse
	errs      []error
	calls     int
	requests  []CompletionRequest
}

func (s *retryStub) Name() string {
	if s.name != "" {
		return s.name
	}
	return "retry-stub"
}

func (s *retryStub) Complete(_ context.Context, req CompletionRequest) (CompletionResponse, error) {
	i := s.calls
	s.calls++
	s.requests = append(s.requests, req)
	if i < len(s.errs) && s.errs[i] != nil {
		return CompletionResponse{}, s.errs[i]
	}
	return s.responses[i], nil
}

const validSummarize = `{"bullets":["b1"],"todos":[],"key_dates":[],"language":"en"}`

// TestCompleteValidatedRetry pins the corrective retry: first answer fails
// the schema, the second call carries the validation error as a user
// message at temperature 0 and its valid payload is served undegraded with
// accumulated usage.
func TestCompleteValidatedRetry(t *testing.T) {
	stub := &retryStub{responses: []CompletionResponse{
		{Content: `{"summary":"legacy shape"}`, UsageKnown: true, PromptTokens: 10, OutputTokens: 5},
		{Content: "```json\n" + validSummarize + "\n```", UsageKnown: true, PromptTokens: 12, OutputTokens: 7},
	}}
	payload, usage, meta, err := CompleteValidated(context.Background(), stub, "summarize", CompletionRequest{Messages: []Message{{Role: "system", Content: "s"}, {Role: "user", Content: "c"}}}, StructuredParams(OperationInput{Operation: "summarize", Body: "b"}))
	if err != nil || meta.Degraded || meta.TemplateVersion != 3 {
		t.Fatalf("payload=%s err=%v meta=%+v", payload, err, meta)
	}
	if stub.calls != 2 || usage.PromptTokens != 22 || usage.OutputTokens != 12 || !usage.Known {
		t.Fatalf("calls=%d usage=%+v", stub.calls, usage)
	}
	corrective := stub.requests[1].Messages[len(stub.requests[1].Messages)-1]
	if stub.requests[1].Temperature == nil || *stub.requests[1].Temperature != 0 ||
		!strings.Contains(corrective.Content, "failed schema validation") || corrective.Role != "user" {
		t.Fatalf("corrective retry request: %+v", stub.requests[1])
	}
	var m map[string]any
	_ = json.Unmarshal(payload, &m)
	if m["bullets"] == nil {
		t.Fatalf("structured payload missing: %s", payload)
	}
}

// TestCompleteValidatedDegrade pins the plain-text fallback: both
// structured attempts fail schema validation, the third call is a
// non-JSON plain-text resend whose answer is served as result.text with
// degraded=true and schema_retry_exhausted.
func TestCompleteValidatedDegrade(t *testing.T) {
	stub := &retryStub{responses: []CompletionResponse{
		{Content: "not json", UsageKnown: true, PromptTokens: 10, OutputTokens: 5},
		{Content: `{"bullets":[]}`, UsageKnown: true, PromptTokens: 8, OutputTokens: 4},
		{Content: "  Invoice total: 42 euros.\n", UsageKnown: true, PromptTokens: 6, OutputTokens: 3},
	}}
	payload, usage, meta, err := CompleteValidated(context.Background(), stub, "summarize", CompletionRequest{Messages: []Message{{Role: "user", Content: "c"}}}, StructuredParams(OperationInput{Operation: "summarize", Body: "b"}))
	if err != nil || !meta.Degraded || meta.DegradeReason != DegradeSchemaRetryExhausted || meta.TemplateVersion != 1 {
		t.Fatalf("err=%v meta=%+v", err, meta)
	}
	if stub.calls != 3 || stub.requests[2].JSONMode || *stub.requests[2].Temperature != 0 {
		t.Fatalf("fallback call: %+v", stub.requests[2])
	}
	var m map[string]any
	_ = json.Unmarshal(payload, &m)
	if m["text"] != "Invoice total: 42 euros." {
		t.Fatalf("degraded text: %s", payload)
	}
	if usage.PromptTokens != 24 || usage.OutputTokens != 12 {
		t.Fatalf("usage across attempts: %+v", usage)
	}
}

// TestCompleteValidatedProviderError pins that provider failures stay
// errors (the handler keeps its AI_UNAVAILABLE path) and mark the meta as
// provider_unavailable without producing a result.
func TestCompleteValidatedProviderError(t *testing.T) {
	stub := &retryStub{errs: []error{ErrInvalidResponse}}
	_, _, meta, err := CompleteValidated(context.Background(), stub, "summarize", CompletionRequest{}, StructuredParams(OperationInput{Operation: "summarize", Body: "b"}))
	if err == nil || !meta.Degraded || meta.DegradeReason != DegradeProviderUnavailable || stub.calls != 1 {
		t.Fatalf("err=%v meta=%+v calls=%d", err, meta, stub.calls)
	}
}

package ai

import (
	"context"
	"encoding/json"
	"errors"
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
	if i >= len(s.responses) {
		return CompletionResponse{}, nil
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

// TestCompleteValidatedProviderErrorDegrades pins the DEF-B contract
// (CHANGELOG #65): a provider error on the structured call is never
// retried as structured and never surfaced as a hard failure — the
// pipeline degrades through the plain-text fallback resend and the user
// gets a labeled usable answer with reason provider_unavailable.
func TestCompleteValidatedProviderErrorDegrades(t *testing.T) {
	stub := &retryStub{
		errs: []error{errors.New("provider connection failed")},
		// Index 0 is consumed by the failed structured attempt; the
		// fallback resend is call number two.
		responses: []CompletionResponse{{}, {Content: "plain fallback answer", UsageKnown: true, PromptTokens: 9, OutputTokens: 4}},
	}
	payload, usage, meta, err := CompleteValidated(context.Background(), stub, "summarize", CompletionRequest{}, StructuredParams(OperationInput{Operation: "summarize", Body: "b"}))
	if err != nil {
		t.Fatalf("provider error must degrade, not fail: %v", err)
	}
	if !meta.Degraded || meta.DegradeReason != DegradeProviderUnavailable {
		t.Fatalf("meta=%+v, want degraded/provider_unavailable", meta)
	}
	if meta.TemplateVersion != fallbackTemplateVersion {
		t.Fatalf("template version %d, want the fallback generation", meta.TemplateVersion)
	}
	var got struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("payload %s: %v", payload, err)
	}
	if got.Text != "plain fallback answer" {
		t.Fatalf("degraded text %q", got.Text)
	}
	if stub.calls != 2 {
		t.Fatalf("calls=%d, want 2 (one structured attempt + one fallback resend)", stub.calls)
	}
	if !usage.Known {
		t.Fatalf("fallback usage must keep the total known")
	}
}

// TestCompleteValidatedProviderHardDown pins the hard boundary of the
// contract: when even the plain-text fallback resend cannot reach the
// provider, the pipeline returns an error (the handler answers 502
// AI_UNAVAILABLE) — there is no usable answer left to degrade to. No
// structured retry happens in between.
func TestCompleteValidatedProviderHardDown(t *testing.T) {
	stub := &retryStub{errs: []error{errors.New("timeout"), errors.New("still down")}}
	_, _, meta, err := CompleteValidated(context.Background(), stub, "summarize", CompletionRequest{}, StructuredParams(OperationInput{Operation: "summarize", Body: "b"}))
	if err == nil {
		t.Fatalf("provider fully down must stay an error")
	}
	if !meta.Degraded || meta.DegradeReason != DegradeProviderUnavailable {
		t.Fatalf("meta=%+v", meta)
	}
	if stub.calls != 2 {
		t.Fatalf("calls=%d, want 2 (one structured attempt + one fallback resend, no more)", stub.calls)
	}
}

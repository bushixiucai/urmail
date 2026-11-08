package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bushixiucai/urmail/internal/ai/templates"
)

// Pipeline is the shared execution path for structured AI operations
// (TECH §2/§8-2): template assembly -> provider call -> JSON fence
// stripping -> strict payload validation -> normalized Result. Retry,
// plain-text degradation and caching hook in around Execute in later PRs;
// the assist endpoint switches to the pipeline in the contract PR, which is
// also when the *_v2 template keys become canonical and the v1 entries go.

// structuredTemplates maps pipeline operation names to the template
// generation that carries the structured output schemas. Keeping the
// mapping explicit makes the v1->v2 rollout a one-table switch.
var structuredTemplates = map[string]string{
	"classify":    "classify_v2",
	"summarize":   "summarize_v2",
	"draft_new":   "draft_new_v2",
	"draft_reply": "draft_reply_v2",
	"polish":      "polish_v2",
	"translate":   "translate_v2",
	"phishing":    "phishing_v2",
	"todos":       "todos_v2",
	"custom":      "custom_v2",
}

// Result is one pipeline execution. Payload is the normalized JSON the API
// layer returns; Degraded marks a non-structured fallback (set by the
// retry/degrade PR); TemplateVersion attributes the output to the exact
// prompt generation.
type Result struct {
	Operation       string
	Payload         json.RawMessage
	TemplateVersion int
	Degraded        bool
	DegradeReason   string
}

// Pipeline executes structured operations against a provider.
type Pipeline struct {
	provider Provider
	model    string
	timeout  time.Duration
}

// PipelineOption configures a Pipeline.
type PipelineOption func(*Pipeline)

// WithPipelineModel sets the target model.
func WithPipelineModel(model string) PipelineOption {
	return func(p *Pipeline) { p.model = model }
}

// WithPipelineTimeout bounds one execution; non-positive values are ignored.
func WithPipelineTimeout(d time.Duration) PipelineOption {
	return func(p *Pipeline) {
		if d > 0 {
			p.timeout = d
		}
	}
}

// NewPipeline creates a Pipeline over the given provider.
func NewPipeline(provider Provider, opts ...PipelineOption) *Pipeline {
	p := &Pipeline{provider: provider, timeout: defaultAssistantTimeout}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Execute runs one operation. The context bounds the whole execution so
// later retry stages can share one deadline budget.
func (p *Pipeline) Execute(ctx context.Context, operation string, params templates.Params) (Result, error) {
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	key, ok := structuredTemplates[operation]
	if !ok {
		return Result{}, fmt.Errorf("%w: pipeline operation %q", ErrInvalidRequest, operation)
	}
	treq, err := templates.Build(key, params)
	if err != nil {
		return Result{}, err
	}
	req := requestFromTemplate(p.model, treq)
	resp, err := p.provider.Complete(ctx, req)
	if err != nil {
		return Result{}, wrapProviderError(ctx, err, "ai pipeline "+operation)
	}
	payload, err := ValidatePayload(operation, extractJSON(resp.Content))
	if err != nil {
		return Result{}, err
	}
	return Result{
		Operation:       operation,
		Payload:         payload,
		TemplateVersion: treq.Version,
	}, nil
}

// requestFromTemplate maps a rendered template request onto the provider
// wire format. Shared by the Assistant and the Pipeline.
func requestFromTemplate(model string, treq templates.Request) CompletionRequest {
	req := CompletionRequest{
		Model:    model,
		JSONMode: treq.JSONMode,
		Messages: make([]Message, len(treq.Messages)),
	}
	if treq.MaxTokens > 0 {
		mt := treq.MaxTokens
		req.MaxTokens = &mt
	}
	if treq.Temperature != nil {
		t := *treq.Temperature
		req.Temperature = &t
	}
	for i, m := range treq.Messages {
		req.Messages[i] = Message{Role: m.Role, Content: m.Content}
	}
	return req
}

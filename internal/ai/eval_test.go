package ai_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	ai "github.com/bushixiucai/urmail/internal/ai"
)

// AI evaluation suite (TECH §2/§3): fixtures under testdata/eval/<op>/
// case-*.json pin the structured contract end to end. In mock mode (the
// default, run by every `go test ./...`) each fixture's recorded provider
// answer flows through the real pipeline — StructuredRequest, prompt
// assembly, extraction, strict validation, normalization, output
// sanitization — and must reproduce the expected normalized payload
// exactly with a single provider call. With URMAIL_AI_EVAL=1 the same
// fixtures grade a live provider: the exact-payload assertion is dropped
// (models vary), checks and the non-degraded contract remain, and the
// pass ratio must clear the threshold (default 0.95 cloud, 0.80 ollama).
//
// The suite lives in the external ai_test package: the mock path shares
// nothing mutable with the library, and gosec's taint analysis (which is
// package-local) then cannot attribute the harness's os.Getenv sources
// to the provider sinks in package ai.

const evalFixtureRoot = "testdata/eval"

// evalOperations mirrors the structured pipeline operation table.
var evalOperations = []string{
	"classify", "summarize", "draft_new", "draft_reply", "polish",
	"translate", "phishing", "todos", "custom",
}

// evalCheck is one assertion against the normalized payload. Path is a
// dot path (map keys and array indexes). Op is one of eq, contains,
// len_ge. Value decodes from JSON, so numbers are float64.
type evalCheck struct {
	Path  string `json:"path"`
	Op    string `json:"op"`
	Value any    `json:"value,omitempty"`
}

type evalExpect struct {
	// Payload is the exact normalized JSON (mock mode only).
	Payload any         `json:"payload,omitempty"`
	Checks  []evalCheck `json:"checks"`
}

type evalCase struct {
	Input      ai.OperationInput `json:"input"`
	MockOutput string            `json:"mock_output"`
	Expect     evalExpect        `json:"expect"`
}

type namedEvalCase struct {
	name string
	file string
	evalCase
}

// evalStubProvider replays a fixture's recorded provider content.
type evalStubProvider struct {
	content string
	calls   int
}

func (p *evalStubProvider) Name() string { return "eval-stub" }

func (p *evalStubProvider) Complete(_ context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
	p.calls++
	return ai.CompletionResponse{
		Content:      p.content,
		PromptTokens: 120,
		OutputTokens: 48,
		TotalTokens:  168,
		UsageKnown:   true,
	}, nil
}

// loadEvalCases reads every fixture grouped by operation directory.
func loadEvalCases(t *testing.T) map[string][]namedEvalCase {
	t.Helper()
	grouped := make(map[string][]namedEvalCase)
	files, err := filepath.Glob(filepath.Join(evalFixtureRoot, "*", "case-*.json"))
	if err != nil {
		t.Fatalf("glob fixtures: %v", err)
	}
	for _, file := range files {
		op := filepath.Base(filepath.Dir(file))
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		var ec evalCase
		if err := json.Unmarshal(raw, &ec); err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		if ec.Input.Operation == "" {
			ec.Input.Operation = op
		}
		if ec.Input.Operation != op {
			t.Fatalf("%s: input operation %q does not match directory %q", file, ec.Input.Operation, op)
		}
		grouped[op] = append(grouped[op], namedEvalCase{
			name:     strings.TrimSuffix(filepath.Base(file), ".json"),
			file:     file,
			evalCase: ec,
		})
	}
	return grouped
}

// TestAIEvalCoverage pins the fixture set itself: every structured
// operation carries exactly three graded cases, and only known
// operations appear.
func TestAIEvalCoverage(t *testing.T) {
	grouped := loadEvalCases(t)
	total := 0
	for op, cases := range grouped {
		if !isEvalOperation(op) {
			t.Fatalf("unknown operation directory %q", op)
		}
		if len(cases) != 3 {
			t.Fatalf("operation %s has %d cases, want 3", op, len(cases))
		}
		total += len(cases)
	}
	if total != 27 {
		t.Fatalf("fixture total %d, want 27 (9 operations x 3)", total)
	}
	for _, op := range evalOperations {
		if len(grouped[op]) == 0 {
			t.Fatalf("operation %s has no fixtures", op)
		}
	}
}

func isEvalOperation(op string) bool {
	for _, known := range evalOperations {
		if op == known {
			return true
		}
	}
	return false
}

// TestAIEvalMockSuite replays every fixture through the real pipeline
// with a stub provider. Exact payload, single provider call, template
// version and non-degraded meta are all pinned per case.
func TestAIEvalMockSuite(t *testing.T) {
	grouped := loadEvalCases(t)
	for _, op := range evalOperations {
		for _, c := range grouped[op] {
			t.Run(op+"/"+c.name, func(t *testing.T) {
				runMockEvalCase(t, op, c)
			})
		}
	}
}

func runMockEvalCase(t *testing.T, op string, c namedEvalCase) {
	t.Helper()
	req, err := ai.StructuredRequest(c.Input)
	if err != nil {
		t.Fatalf("structured request: %v", err)
	}
	provider := &evalStubProvider{content: c.MockOutput}
	payload, usage, meta, err := ai.CompleteValidated(
		context.Background(), provider, op, req, ai.StructuredParams(c.Input))
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if meta.Degraded {
		t.Fatalf("mock output degraded the pipeline: %+v", meta)
	}
	if want := ai.StructuredTemplateVersion(op); meta.TemplateVersion != want {
		t.Fatalf("template version %d, want %d", meta.TemplateVersion, want)
	}
	if !usage.Known {
		t.Fatalf("usage unknown on a known-usage stub")
	}
	if provider.calls != 1 {
		t.Fatalf("provider called %d times, want 1 (mock output must validate first try)", provider.calls)
	}
	if c.Expect.Payload != nil {
		var got any
		if err := json.Unmarshal(payload, &got); err != nil {
			t.Fatalf("normalized payload not JSON: %v (%s)", err, payload)
		}
		if !reflect.DeepEqual(got, c.Expect.Payload) {
			want, _ := json.Marshal(c.Expect.Payload)
			t.Fatalf("normalized payload mismatch\n got: %s\nwant: %s", payload, want)
		}
	}
	for _, chk := range c.Expect.Checks {
		if err := runEvalCheck(payload, chk); err != nil {
			t.Fatalf("check %s %s %v: %v (payload %s)", chk.Path, chk.Op, chk.Value, err, payload)
		}
	}
}

// runEvalCheck evaluates one assertion against the normalized payload.
func runEvalCheck(payload json.RawMessage, chk evalCheck) error {
	var doc any
	if err := json.Unmarshal(payload, &doc); err != nil {
		return fmt.Errorf("payload not JSON: %w", err)
	}
	actual, ok := evalPath(doc, chk.Path)
	if !ok {
		return fmt.Errorf("path %q not found", chk.Path)
	}
	switch chk.Op {
	case "eq":
		if !reflect.DeepEqual(actual, chk.Value) {
			return fmt.Errorf("got %#v", actual)
		}
		return nil
	case "contains":
		if s, ok := actual.(string); ok {
			if strings.Contains(s, fmt.Sprint(chk.Value)) {
				return nil
			}
			return fmt.Errorf("string %q lacks %q", s, chk.Value)
		}
		if list, ok := actual.([]any); ok {
			for _, item := range list {
				if s, ok := item.(string); ok && strings.Contains(s, fmt.Sprint(chk.Value)) {
					return nil
				}
			}
			return fmt.Errorf("no element contains %q", chk.Value)
		}
		return fmt.Errorf("contains needs a string or array, got %T", actual)
	case "len_ge":
		want := int(reflect.ValueOf(chk.Value).Float())
		if !isLenable(actual) {
			return fmt.Errorf("len_ge needs an array, got %T", actual)
		}
		if got := reflect.ValueOf(actual).Len(); got < want {
			return fmt.Errorf("length %d, want >= %d", got, want)
		}
		return nil
	default:
		return fmt.Errorf("unknown check op %q", chk.Op)
	}
}

func isLenable(v any) bool {
	switch v.(type) {
	case []any, map[string]any, string:
		return true
	}
	return false
}

// evalPath walks a dot path through maps and arrays.
func evalPath(doc any, path string) (any, bool) {
	cur := doc
	var ok bool
	for _, seg := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			cur, ok = node[seg]
			if !ok {
				return nil, false
			}
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, false
			}
			cur = node[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

// TestAIEvalRealProvider grades the fixtures against a live provider.
// URMAIL_AI_EVAL=1 enables it; provider selection:
//
//	URMAIL_AI_EVAL_PROVIDER  openai (default) | ollama
//	URMAIL_AI_EVAL_API_KEY   cloud API key (openai)
//	URMAIL_AI_EVAL_BASE_URL  OpenAI-compatible endpoint override
//	URMAIL_AI_EVAL_MODEL     default model override
//	URMAIL_AI_EVAL_THRESHOLD pass-ratio gate (default 0.95 cloud, 0.80 ollama)
//
// The API key is read from the environment only and is never logged.
func TestAIEvalRealProvider(t *testing.T) {
	if os.Getenv("URMAIL_AI_EVAL") != "1" {
		t.Skip("live-provider grading disabled; set URMAIL_AI_EVAL=1")
	}
	providerName := os.Getenv("URMAIL_AI_EVAL_PROVIDER")
	if providerName == "" {
		providerName = ai.ProviderOpenAI
	}
	threshold := 0.95
	if providerName == ai.ProviderOllama {
		threshold = 0.80
	}
	if raw := os.Getenv("URMAIL_AI_EVAL_THRESHOLD"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || v < 0 || v > 1 {
			t.Fatalf("invalid URMAIL_AI_EVAL_THRESHOLD %q", raw)
		}
		threshold = v
	}
	perCase := 90 * time.Second
	if providerName == ai.ProviderOllama {
		perCase = 240 * time.Second
	}
	if raw := os.Getenv("URMAIL_AI_EVAL_TIMEOUT_SECONDS"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			perCase = time.Duration(v) * time.Second
		}
	}

	var provider ai.Provider
	switch providerName {
	case ai.ProviderOpenAI:
		key := os.Getenv("URMAIL_AI_EVAL_API_KEY")
		if key == "" {
			t.Fatal("URMAIL_AI_EVAL_API_KEY is required for the openai eval provider")
		}
		var opts []ai.OpenAIOption
		if model := os.Getenv("URMAIL_AI_EVAL_MODEL"); model != "" {
			opts = append(opts, ai.WithOpenAIDefaultModel(model))
		}
		if raw := os.Getenv("URMAIL_AI_EVAL_BASE_URL"); raw != "" {
			opts = append(opts, evalOpenAIEndpointOpts(t, raw)...)
		}
		p, err := ai.NewOpenAIProvider(key, opts...)
		if err != nil {
			t.Fatalf("openai provider: %v", err)
		}
		provider = p
	case ai.ProviderOllama:
		var opts []ai.OllamaOption
		if raw := os.Getenv("URMAIL_AI_EVAL_BASE_URL"); raw != "" {
			opts = append(opts, ai.WithOllamaBaseURL(raw))
		}
		if model := os.Getenv("URMAIL_AI_EVAL_MODEL"); model != "" {
			opts = append(opts, ai.WithOllamaModel(model))
		}
		p, err := ai.NewOllamaProvider(opts...)
		if err != nil {
			t.Fatalf("ollama provider: %v", err)
		}
		provider = p
	default:
		t.Fatalf("unknown URMAIL_AI_EVAL_PROVIDER %q", providerName)
	}

	grouped := loadEvalCases(t)
	pass, total := 0, 0
	for _, op := range evalOperations {
		for _, c := range grouped[op] {
			total++
			if ok, why := runRealEvalCase(context.Background(), provider, op, c, perCase); ok {
				pass++
			} else {
				t.Logf("FAIL %s/%s: %s", op, c.name, why)
			}
		}
	}
	ratio := float64(pass) / math.Max(float64(total), 1)
	t.Logf("live eval: %d/%d passed, ratio %.3f, threshold %.3f (%s)", pass, total, ratio, threshold, provider.Name())
	if ratio < threshold {
		t.Errorf("live eval ratio %.3f below threshold %.3f", ratio, threshold)
	}
}

// runRealEvalCase grades one fixture against the live provider: the
// pipeline must produce a validated, non-degraded result whose checks
// pass. The exact payload is not compared — real models vary in wording.
func runRealEvalCase(parent context.Context, provider ai.Provider, op string, c namedEvalCase, perCase time.Duration) (bool, string) {
	ctx, cancel := context.WithTimeout(parent, perCase)
	defer cancel()
	req, err := ai.StructuredRequest(c.Input)
	if err != nil {
		return false, fmt.Sprintf("structured request: %v", err)
	}
	payload, _, meta, err := ai.CompleteValidated(ctx, provider, op, req, ai.StructuredParams(c.Input))
	if err != nil {
		if meta.Degraded {
			return false, "degraded: " + err.Error()
		}
		return false, "pipeline error: " + err.Error()
	}
	if meta.Degraded {
		return false, "degraded (schema retry exhausted): " + meta.DegradeReason
	}
	for _, chk := range c.Expect.Checks {
		if err := runEvalCheck(payload, chk); err != nil {
			return false, fmt.Sprintf("check %s %s %v: %v", chk.Path, chk.Op, chk.Value, err)
		}
	}
	return true, ""
}

// evalOpenAIEndpointOpts builds the endpoint-policy options for a custom
// OpenAI-compatible eval endpoint: its host is allowlisted explicitly,
// and plain-HTTP or reserved hosts are permitted only when the operator
// pointed the eval at them on purpose.
func evalOpenAIEndpointOpts(t *testing.T, raw string) []ai.OpenAIOption {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		t.Fatalf("invalid URMAIL_AI_EVAL_BASE_URL %q", raw)
	}
	opts := []ai.OpenAIOption{ai.WithOpenAIBaseURL(raw), ai.WithOpenAIAllowedHosts(u.Hostname())}
	if u.Scheme == "http" {
		opts = append(opts, ai.WithOpenAIAllowInsecureHTTP())
	}
	host := u.Hostname()
	if host == "localhost" || host == "::1" || strings.HasPrefix(host, "127.") {
		opts = append(opts, ai.WithOpenAIAllowReservedHosts(host))
	}
	if port := u.Port(); port != "" && port != "443" {
		n, err := strconv.Atoi(port)
		if err == nil {
			opts = append(opts, ai.WithOpenAIAllowedPorts(n))
		}
	}
	return opts
}

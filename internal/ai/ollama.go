package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultOllamaBaseURL       = "http://127.0.0.1:11434"
	defaultOllamaModel         = "llama3"
	defaultOllamaClientTimeout = 60 * time.Second
)

// isLoopbackHost checks whether a hostname or IP address strictly refers to local loopback.
func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	host = strings.Trim(host, "[]")
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// validateLocalEndpoint validates that the URL strictly targets a local loopback address (P06 local-only rule).
func validateLocalEndpoint(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: malformed local endpoint %q: %w", ErrLocalOnly, rawURL, err)
	}
	host := u.Hostname()
	if !isLoopbackHost(host) {
		return fmt.Errorf("%w: endpoint %q specifies non-loopback host %q", ErrLocalOnly, rawURL, host)
	}
	return nil
}

// newLocalOnlyTransport creates an http.Transport strictly pinned to loopback addresses with zero external leakage.
func newLocalOnlyTransport() *http.Transport {
	return &http.Transport{
		Proxy: nil, // Disallow proxies to prevent egress leakage
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid target address %q: %w", ErrLocalOnly, addr, err)
			}
			if !isLoopbackHost(host) {
				ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
				if err != nil {
					return nil, fmt.Errorf("%w: dns lookup failed for %q: %w", ErrLocalOnly, host, err)
				}
				if len(ips) == 0 {
					return nil, fmt.Errorf("%w: no ips resolved for host %q", ErrLocalOnly, host)
				}
				for _, ip := range ips {
					if !ip.IsLoopback() {
						return nil, fmt.Errorf("%w: connection to non-loopback IP %s for host %q disallowed", ErrLocalOnly, ip.String(), host)
					}
				}
			}
			var d net.Dialer
			return d.DialContext(ctx, network, net.JoinHostPort(host, port))
		},
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// OllamaProvider implements the Provider interface for local Ollama instances.
type OllamaProvider struct {
	name         string
	baseURL      string
	defaultModel string
	client       *http.Client
}

// OllamaOption configures an OllamaProvider.
type OllamaOption func(*OllamaProvider) error

// WithOllamaBaseURL sets the base URL for the local Ollama instance (must be loopback).
func WithOllamaBaseURL(baseURL string) OllamaOption {
	return func(p *OllamaProvider) error {
		trimmed := strings.TrimRight(baseURL, "/")
		if err := validateLocalEndpoint(trimmed); err != nil {
			return err
		}
		p.baseURL = trimmed
		return nil
	}
}

// WithOllamaModel sets the default model name.
func WithOllamaModel(model string) OllamaOption {
	return func(p *OllamaProvider) error {
		if model != "" {
			p.defaultModel = model
		}
		return nil
	}
}

// WithOllamaHTTPClient allows specifying a custom client (still subject to local loopback constraints).
func WithOllamaHTTPClient(client *http.Client) OllamaOption {
	return func(p *OllamaProvider) error {
		if client != nil {
			p.client = client
		}
		return nil
	}
}

// NewOllamaProvider creates a local-only Ollama provider enforcing P06 rules.
func NewOllamaProvider(opts ...OllamaOption) (*OllamaProvider, error) {
	p := &OllamaProvider{
		name:         ProviderOllama,
		baseURL:      defaultOllamaBaseURL,
		defaultModel: defaultOllamaModel,
		client: &http.Client{
			Transport: newLocalOnlyTransport(),
			Timeout:   defaultOllamaClientTimeout,
		},
	}
	for _, opt := range opts {
		if err := opt(p); err != nil {
			return nil, err
		}
	}
	// Enforce local-only rule on the finalized baseURL
	if err := validateLocalEndpoint(p.baseURL); err != nil {
		return nil, err
	}
	return p, nil
}

// Name returns the provider identifier.
func (p *OllamaProvider) Name() string {
	return p.name
}

type ollamaChatRequest struct {
	Model    string         `json:"model"`
	Messages []Message      `json:"messages"`
	Stream   bool           `json:"stream"`
	Format   string         `json:"format,omitempty"`
	Options  map[string]any `json:"options,omitempty"`
}

type ollamaChatResponse struct {
	Model           string  `json:"model"`
	CreatedAt       string  `json:"created_at"`
	Message         Message `json:"message"`
	Done            bool    `json:"done"`
	DoneReason      string  `json:"done_reason"`
	TotalDuration   int64   `json:"total_duration"`
	PromptEvalCount int     `json:"prompt_eval_count"`
	EvalCount       int     `json:"eval_count"`
	Error           string  `json:"error,omitempty"`
}

// Complete sends a completion request to local Ollama.
func (p *OllamaProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	// Re-verify local loopback rule on every call to prevent runtime URL tampering
	if err := validateLocalEndpoint(p.baseURL); err != nil {
		return CompletionResponse{}, err
	}

	msgs := req.EffectiveMessages()
	if len(msgs) == 0 {
		return CompletionResponse{}, ErrEmptyPrompt
	}

	model := req.Model
	if model == "" {
		model = p.defaultModel
	}

	isOpenAIPath := strings.Contains(p.baseURL, "/v1")
	if isOpenAIPath {
		return p.completeOpenAICompat(ctx, model, msgs, req)
	}

	options := make(map[string]any)
	if req.Temperature != nil {
		options["temperature"] = *req.Temperature
	}
	if req.MaxTokens != nil {
		options["num_predict"] = *req.MaxTokens
	}

	ollamaReq := ollamaChatRequest{
		Model:    model,
		Messages: msgs,
		Stream:   false,
		Options:  options,
	}
	if req.JSONMode {
		ollamaReq.Format = "json"
	}

	payloadBytes, err := json.Marshal(ollamaReq)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("failed to marshal ollama request: %w", err)
	}

	endpoint := p.baseURL + "/api/chat"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("failed to create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("ollama local request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return CompletionResponse{}, fmt.Errorf("%w: status %d: %s", ErrProviderError, resp.StatusCode, string(bodyBytes))
	}

	var chatResp ollamaChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return CompletionResponse{}, fmt.Errorf("%w: failed to parse ollama json: %w", ErrInvalidResponse, err)
	}

	if chatResp.Error != "" {
		return CompletionResponse{}, fmt.Errorf("%w: %s", ErrProviderError, chatResp.Error)
	}

	finishReason := chatResp.DoneReason
	if finishReason == "" && chatResp.Done {
		finishReason = "stop"
	}

	return CompletionResponse{
		Content:      chatResp.Message.Content,
		Text:         chatResp.Message.Content,
		FinishReason: finishReason,
		PromptTokens: chatResp.PromptEvalCount,
		OutputTokens: chatResp.EvalCount,
		TotalTokens:  chatResp.PromptEvalCount + chatResp.EvalCount,
	}, nil
}

func (p *OllamaProvider) completeOpenAICompat(ctx context.Context, model string, msgs []Message, req CompletionRequest) (CompletionResponse, error) {
	endpoint := strings.TrimRight(p.baseURL, "/")
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint += "/chat/completions"
	}
	reqBody := openAIReqBody{
		Model:       model,
		Messages:    msgs,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}
	if req.JSONMode {
		reqBody.ResponseFormat = &openAIResponseFormat{Type: "json_object"}
	}
	payloadBytes, err := json.Marshal(reqBody)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("failed to marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("failed to create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("ollama request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("failed to read response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return CompletionResponse{}, fmt.Errorf("%w: status %d: %s", ErrProviderError, resp.StatusCode, string(bodyBytes))
	}
	var chatResp openAIChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return CompletionResponse{}, fmt.Errorf("%w: %w", ErrInvalidResponse, err)
	}
	if len(chatResp.Choices) == 0 {
		return CompletionResponse{}, fmt.Errorf("%w: empty choices", ErrInvalidResponse)
	}
	firstChoice := chatResp.Choices[0]
	return CompletionResponse{
		Content:      firstChoice.Message.Content,
		Text:         firstChoice.Message.Content,
		FinishReason: firstChoice.FinishReason,
		PromptTokens: chatResp.Usage.PromptTokens,
		OutputTokens: chatResp.Usage.CompletionTokens,
		TotalTokens:  chatResp.Usage.TotalTokens,
	}, nil
}

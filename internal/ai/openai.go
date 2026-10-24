package ai

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultOpenAIBaseURL       = "https://api.openai.com/v1"
	defaultOpenAIModel         = "gpt-4o-mini"
	defaultOpenAIClientTimeout = 60 * time.Second
)

// defaultOpenAIAllowedHosts is the built-in endpoint allowlist. Only hosts on
// this list (or on a list extended by the operator) are ever contacted, so a
// misconfigured or attacker-supplied base URL cannot be used to reach
// internal services such as the cloud metadata endpoint.
var defaultOpenAIAllowedHosts = []string{"api.openai.com"}

// defaultOpenAIAllowedPorts lists the ports that are accepted without an
// explicit operator opt-in. Any other port has to be allowlisted with
// WithOpenAIAllowedPorts.
var defaultOpenAIAllowedPorts = []int{443, 80}

// Endpoint policy violations.
var (
	// ErrOpenAIEndpointNotAllowed is returned when the configured endpoint is
	// not covered by the endpoint allowlist.
	ErrOpenAIEndpointNotAllowed = errors.New("openai endpoint is not allowed by policy")
	// ErrOpenAISchemeNotAllowed is returned when the endpoint scheme is not
	// https (or http without the explicit insecure opt-in).
	ErrOpenAISchemeNotAllowed = errors.New("openai endpoint scheme is not allowed by policy")
	// ErrOpenAIPortNotAllowed is returned when the endpoint port is not on the
	// port allowlist.
	ErrOpenAIPortNotAllowed = errors.New("openai endpoint port is not allowed by policy")
	// ErrOpenAIReservedAddress is returned when the endpoint host resolves to a
	// loopback, link-local, private or otherwise reserved address.
	ErrOpenAIReservedAddress = errors.New("openai endpoint resolves to a reserved address")
	// ErrOpenAIRedirectBlocked is returned when an upstream redirect is
	// refused by the redirect policy.
	ErrOpenAIRedirectBlocked = errors.New("openai endpoint redirect was refused")
)

// openAIHostResolver resolves a hostname to the addresses that may be
// connected to. It is a field on the provider so tests can pin resolution.
type openAIHostResolver func(ctx context.Context, host string) ([]net.IP, error)

// defaultOpenAIResolver resolves a hostname with the system resolver.
func defaultOpenAIResolver(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// openAIEndpointPolicy is the endpoint allowlist enforced by the provider.
type openAIEndpointPolicy struct {
	// hosts is the set of allowlisted hostnames (normalised, lower case).
	hosts map[string]struct{}
	// reservedHosts is the subset of hosts that the operator explicitly
	// allowed to resolve to loopback, link-local, private or otherwise
	// reserved addresses.
	reservedHosts map[string]struct{}
	// ports is the set of allowlisted TCP ports.
	ports map[int]struct{}
	// allowHTTP enables plain http (off by default: https only).
	allowHTTP bool
	// allowRedirect enables following redirects (off by default). When
	// enabled, the redirect target is re-validated against this policy.
	allowRedirect bool
}

// newOpenAIEndpointPolicy returns the default endpoint policy.
func newOpenAIEndpointPolicy() *openAIEndpointPolicy {
	p := &openAIEndpointPolicy{
		hosts:         make(map[string]struct{}, len(defaultOpenAIAllowedHosts)),
		reservedHosts: make(map[string]struct{}),
		ports:         make(map[int]struct{}, len(defaultOpenAIAllowedPorts)),
	}
	for _, h := range defaultOpenAIAllowedHosts {
		p.hosts[normalizeOpenAIHost(h)] = struct{}{}
	}
	for _, port := range defaultOpenAIAllowedPorts {
		p.ports[port] = struct{}{}
	}
	return p
}

// hostAllowed reports whether the host is on the endpoint allowlist.
func (p *openAIEndpointPolicy) hostAllowed(host string) bool {
	_, ok := p.hosts[normalizeOpenAIHost(host)]
	return ok
}

// reservedAllowed reports whether the host may resolve to a reserved address.
func (p *openAIEndpointPolicy) reservedAllowed(host string) bool {
	_, ok := p.reservedHosts[normalizeOpenAIHost(host)]
	return ok
}

// portAllowed reports whether the port is on the port allowlist.
func (p *openAIEndpointPolicy) portAllowed(port int) bool {
	_, ok := p.ports[port]
	return ok
}

// normalizeOpenAIHost normalises a hostname for allowlist comparisons: it is
// lower cased, stripped of IPv6 brackets and of the root dot, and IP literals
// are canonicalised.
func normalizeOpenAIHost(host string) string {
	h := strings.TrimSpace(host)
	h = strings.Trim(h, "[]")
	h = strings.ToLower(h)
	h = strings.TrimSuffix(h, ".")
	if ip := net.ParseIP(h); ip != nil {
		return ip.String()
	}
	return h
}

// isReservedOpenAIAddr reports whether an address is loopback, link-local,
// private or otherwise reserved, i.e. an address that must never be reached
// by an AI provider request unless the operator explicitly allowed it.
func isReservedOpenAIAddr(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsUnspecified() ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() {
		return true
	}
	v4 := ip.To4()
	if v4 == nil {
		// IPv6 unique local addresses (fc00::/7) are covered by IsPrivate.
		return false
	}
	switch {
	case v4[0] == 0:
		// 0.0.0.0/8 "this network".
		return true
	case v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127:
		// 100.64.0.0/10 shared address space (CGNAT).
		return true
	case v4[0] == 192 && v4[1] == 0 && v4[2] == 0:
		// 192.0.0.0/24 IETF protocol assignments.
		return true
	case v4[0] == 192 && v4[1] == 0 && v4[2] == 2:
		// 192.0.2.0/24 documentation (TEST-NET-1).
		return true
	case v4[0] == 198 && (v4[1] == 18 || v4[1] == 19):
		// 198.18.0.0/15 benchmarking.
		return true
	case v4[0] == 198 && v4[1] == 51 && v4[2] == 100:
		// 198.51.100.0/24 documentation (TEST-NET-2).
		return true
	case v4[0] == 203 && v4[1] == 0 && v4[2] == 113:
		// 203.0.113.0/24 documentation (TEST-NET-3).
		return true
	case v4[0] >= 240:
		// 240.0.0.0/4 reserved and 255.255.255.255 broadcast.
		return true
	}
	return false
}

// OpenAIProvider implements the Provider interface for OpenAI-compatible APIs.
type OpenAIProvider struct {
	name         string
	baseURL      string
	apiKey       string
	defaultModel string
	client       *http.Client
	customClient *http.Client
	policy       *openAIEndpointPolicy
	resolve      openAIHostResolver
}

// OpenAIOption configures an OpenAIProvider.
type OpenAIOption func(*OpenAIProvider) error

// WithOpenAIBaseURL overrides the base API endpoint.
//
// The URL must pass the endpoint policy: https (or http with the explicit
// insecure opt-in), a host on the endpoint allowlist and a port on the port
// allowlist. Anything else is rejected when the provider is created.
func WithOpenAIBaseURL(baseURL string) OpenAIOption {
	return func(p *OpenAIProvider) error {
		p.baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		return nil
	}
}

// WithOpenAIDefaultModel sets the fallback model name.
func WithOpenAIDefaultModel(model string) OpenAIOption {
	return func(p *OpenAIProvider) error {
		if model != "" {
			p.defaultModel = model
		}
		return nil
	}
}

// WithOpenAIHTTPClient specifies a custom HTTP client.
//
// The client is copied and its redirect policy is replaced by the provider
// policy, so redirects can never move a request to a host outside the
// allowlist. A custom *http.Transport keeps its TLS and timeout settings but
// is dialed through the pinning dialer of the provider. An ambient proxy is
// not supported: a proxy resolves the endpoint itself and would break the
// pinned address guarantee.
func WithOpenAIHTTPClient(client *http.Client) OpenAIOption {
	return func(p *OpenAIProvider) error {
		if client != nil {
			p.customClient = client
		}
		return nil
	}
}

// WithOpenAIName sets a custom identifier for the provider.
func WithOpenAIName(name string) OpenAIOption {
	return func(p *OpenAIProvider) error {
		if name != "" {
			p.name = name
		}
		return nil
	}
}

// WithOpenAIAllowedHosts extends the endpoint allowlist.
//
// Only allowlisted hosts are contacted. Hosts added here may still not
// resolve to reserved addresses unless they are also passed to
// WithOpenAIAllowReservedHosts.
func WithOpenAIAllowedHosts(hosts ...string) OpenAIOption {
	return func(p *OpenAIProvider) error {
		for _, host := range hosts {
			normalized := normalizeOpenAIHost(host)
			if normalized == "" {
				return fmt.Errorf("%w: empty host in allowlist", ErrOpenAIEndpointNotAllowed)
			}
			p.policy.hosts[normalized] = struct{}{}
		}
		return nil
	}
}

// WithOpenAIAllowReservedHosts allowlists the given hosts and additionally
// permits them to resolve to loopback, link-local, private or otherwise
// reserved addresses.
//
// This is the explicit operator opt-in for on-premise gateways that are not
// reachable on the public internet.
func WithOpenAIAllowReservedHosts(hosts ...string) OpenAIOption {
	return func(p *OpenAIProvider) error {
		for _, host := range hosts {
			normalized := normalizeOpenAIHost(host)
			if normalized == "" {
				return fmt.Errorf("%w: empty host in allowlist", ErrOpenAIEndpointNotAllowed)
			}
			p.policy.hosts[normalized] = struct{}{}
			p.policy.reservedHosts[normalized] = struct{}{}
		}
		return nil
	}
}

// WithOpenAIAllowedPorts extends the port allowlist. Ports other than the
// standard ones (443 for https, 80 for http) have to be allowlisted
// explicitly.
func WithOpenAIAllowedPorts(ports ...int) OpenAIOption {
	return func(p *OpenAIProvider) error {
		for _, port := range ports {
			if port < 1 || port > 65535 {
				return fmt.Errorf("%w: invalid port %d", ErrOpenAIPortNotAllowed, port)
			}
			p.policy.ports[port] = struct{}{}
		}
		return nil
	}
}

// WithOpenAIAllowInsecureHTTP permits plain http endpoints. Without this
// opt-in a misconfiguration can never silently downgrade the connection to
// cleartext.
func WithOpenAIAllowInsecureHTTP() OpenAIOption {
	return func(p *OpenAIProvider) error {
		p.policy.allowHTTP = true
		return nil
	}
}

// WithOpenAIAllowRedirects permits following upstream redirects.
//
// Redirects are refused by default because a redirect can move a request
// (including the Authorization header) to a host the operator never
// allowlisted. When this option is set, the redirect target is re-validated
// against the endpoint policy before it is followed.
func WithOpenAIAllowRedirects() OpenAIOption {
	return func(p *OpenAIProvider) error {
		p.policy.allowRedirect = true
		return nil
	}
}

// NewOpenAIProvider creates an OpenAI compatible provider instance.
//
// The provider is created with an endpoint policy: only allowlisted hosts on
// allowlisted ports are contacted, resolved addresses are pinned so they
// cannot be swapped after validation and redirects are refused.
func NewOpenAIProvider(apiKey string, opts ...OpenAIOption) (*OpenAIProvider, error) {
	p := &OpenAIProvider{
		name:         ProviderOpenAI,
		baseURL:      defaultOpenAIBaseURL,
		apiKey:       strings.TrimSpace(apiKey),
		defaultModel: defaultOpenAIModel,
		policy:       newOpenAIEndpointPolicy(),
		resolve:      defaultOpenAIResolver,
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(p); err != nil {
			return nil, p.redactError(err)
		}
	}
	// Enforce the endpoint policy on the finalized base URL.
	if err := p.validateBaseURL(); err != nil {
		return nil, p.redactError(err)
	}
	p.client = p.buildClient()
	return p, nil
}

// Name returns the provider identifier.
func (p *OpenAIProvider) Name() string {
	return p.name
}

type openAIReqBody struct {
	Model          string                `json:"model"`
	Messages       []Message             `json:"messages"`
	Temperature    *float64              `json:"temperature,omitempty"`
	MaxTokens      *int                  `json:"max_tokens,omitempty"`
	ResponseFormat *openAIResponseFormat `json:"response_format,omitempty"`
}

type openAIResponseFormat struct {
	Type string `json:"type"`
}

type openAIChatResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Index        int     `json:"index"`
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// Complete sends a chat completion request to the OpenAI-compatible endpoint.
func (p *OpenAIProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	// Re-verify the endpoint policy on every call so a mutated base URL
	// cannot escape the allowlist at runtime.
	endpoint, err := p.buildEndpoint()
	if err != nil {
		return CompletionResponse{}, safeOpenAIRequestError(err)
	}

	msgs := req.EffectiveMessages()
	if len(msgs) == 0 {
		return CompletionResponse{}, ErrEmptyPrompt
	}

	model := req.Model
	if model == "" {
		model = p.defaultModel
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
		return CompletionResponse{}, fmt.Errorf("%w: cannot encode completion request", ErrInvalidRequest)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("%w: cannot build completion request", ErrInvalidRequest)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	httpResp, err := p.client.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, safeOpenAIRequestError(err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	// Error bodies are untrusted and may echo arbitrary fragments, transformed
	// prompts, or credentials. No upstream body text belongs in an error.
	if httpResp.StatusCode != http.StatusOK {
		kind := ErrProviderError
		switch httpResp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			kind = ErrUnauthorized
		case http.StatusTooManyRequests:
			kind = ErrRateLimitExceeded
		}
		return CompletionResponse{}, fmt.Errorf("%w: status %d", kind, httpResp.StatusCode)
	}

	const maxCompletionResponseBytes = 8 << 20
	bodyBytes, err := io.ReadAll(io.LimitReader(httpResp.Body, maxCompletionResponseBytes+1))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return CompletionResponse{}, safeOpenAIRequestError(err)
		}
		if ctx.Err() != nil {
			return CompletionResponse{}, safeOpenAIRequestError(ctx.Err())
		}
		return CompletionResponse{}, fmt.Errorf("%w: cannot read completion response", ErrInvalidResponse)
	}
	if len(bodyBytes) > maxCompletionResponseBytes {
		return CompletionResponse{}, fmt.Errorf("%w: completion response exceeds limit", ErrInvalidResponse)
	}

	var chatResp openAIChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return CompletionResponse{}, fmt.Errorf("%w: cannot decode completion response", ErrInvalidResponse)
	}

	if chatResp.Error != nil {
		return CompletionResponse{}, fmt.Errorf("%w: completion error envelope", ErrProviderError)
	}

	if len(chatResp.Choices) == 0 {
		return CompletionResponse{}, fmt.Errorf("%w: choices array is empty", ErrInvalidResponse)
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

// safeOpenAIRequestError preserves only approved classifications, never an
// arbitrary transport, resolver, URL or proxy error string/cause.
func safeOpenAIRequestError(err error) error {
	var classifications []error
	for _, kind := range []error{
		context.Canceled, context.DeadlineExceeded,
		ErrOpenAIEndpointNotAllowed, ErrOpenAISchemeNotAllowed,
		ErrOpenAIPortNotAllowed, ErrOpenAIReservedAddress, ErrOpenAIRedirectBlocked,
	} {
		if errors.Is(err, kind) {
			classifications = append(classifications, kind)
		}
	}
	if len(classifications) == 0 {
		classifications = append(classifications, ErrProviderError)
	}
	return fmt.Errorf("%w: completion request failed", errors.Join(classifications...))
}

// openAIRedactedError hides a secret from an error message while keeping the
// error chain intact for errors.Is.
type openAIRedactedError struct {
	msg string
	err error
}

// Error returns the redacted error message.
func (e *openAIRedactedError) Error() string {
	return e.msg
}

// Is reports whether the wrapped error matches the target.
func (e *openAIRedactedError) Is(target error) bool {
	return errors.Is(e.err, target)
}

// openAIRedactor removes API keys from local configuration diagnostics.
// Upstream and request-processing errors use safe classifications instead
// of attempting to redact arbitrary content with string replacement.
type openAIRedactor struct {
	secrets []string
}

// newOpenAIRedactor returns a redactor for the given API key.
func newOpenAIRedactor(apiKey string) *openAIRedactor {
	r := &openAIRedactor{}
	if apiKey != "" {
		r.secrets = append(r.secrets, apiKey)
	}
	return r
}

// redact removes every registered secret from a message.
func (r *openAIRedactor) redact(msg string) string {
	for _, secret := range r.secrets {
		msg = strings.ReplaceAll(msg, secret, "[redacted]")
	}
	return msg
}

// wrap returns an error whose message no longer contains a secret.
func (r *openAIRedactor) wrap(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	redacted := r.redact(msg)
	if redacted == msg {
		return err
	}
	return &openAIRedactedError{msg: redacted, err: err}
}

// redactError removes the API key from an error message.
func (p *OpenAIProvider) redactError(err error) error {
	return newOpenAIRedactor(p.apiKey).wrap(err)
}

// buildEndpoint returns the validated chat completion endpoint.
func (p *OpenAIProvider) buildEndpoint() (string, error) {
	base := strings.TrimRight(p.baseURL, "/")
	var endpoint string
	switch {
	case strings.HasSuffix(base, "/chat/completions"):
		endpoint = base
	case strings.HasSuffix(base, "/v1"):
		endpoint = base + "/chat/completions"
	default:
		endpoint = base + "/v1/chat/completions"
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("%w: malformed endpoint url: %w", ErrOpenAIEndpointNotAllowed, err)
	}
	if err := p.validateEndpoint(u); err != nil {
		return "", err
	}
	return endpoint, nil
}

// validateBaseURL enforces the endpoint policy on the configured base URL.
func (p *OpenAIProvider) validateBaseURL() error {
	u, err := url.Parse(p.baseURL)
	if err != nil {
		return fmt.Errorf("%w: malformed base url: %w", ErrOpenAIEndpointNotAllowed, err)
	}
	return p.validateEndpoint(u)
}

// validateEndpoint enforces the scheme, host and port policy of an endpoint
// URL.
func (p *OpenAIProvider) validateEndpoint(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("%w: missing endpoint url", ErrOpenAIEndpointNotAllowed)
	}
	if u.User != nil {
		return fmt.Errorf("%w: endpoint must not contain userinfo", ErrOpenAIEndpointNotAllowed)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !p.policy.allowHTTP {
			return fmt.Errorf("%w: scheme %q requires the explicit insecure-http opt-in", ErrOpenAISchemeNotAllowed, u.Scheme)
		}
	default:
		return fmt.Errorf("%w: scheme %q is not supported, https is required", ErrOpenAISchemeNotAllowed, u.Scheme)
	}

	host := normalizeOpenAIHost(u.Hostname())
	if host == "" {
		return fmt.Errorf("%w: endpoint has no host", ErrOpenAIEndpointNotAllowed)
	}
	if !p.policy.hostAllowed(host) {
		return fmt.Errorf("%w: host %q is not on the endpoint allowlist", ErrOpenAIEndpointNotAllowed, host)
	}

	port, err := openAIEndpointPort(u)
	if err != nil {
		return err
	}
	if !p.policy.portAllowed(port) {
		return fmt.Errorf("%w: port %d is not on the endpoint port allowlist", ErrOpenAIPortNotAllowed, port)
	}

	// An IP literal host can be checked for reserved ranges without DNS.
	if ip := net.ParseIP(host); ip != nil && !p.policy.reservedAllowed(host) && isReservedOpenAIAddr(ip) {
		return fmt.Errorf("%w: host %q is a reserved address", ErrOpenAIReservedAddress, host)
	}
	return nil
}

// openAIEndpointPort returns the effective port of an endpoint URL.
func openAIEndpointPort(u *url.URL) (int, error) {
	if raw := u.Port(); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			return 0, fmt.Errorf("%w: invalid port %q", ErrOpenAIPortNotAllowed, raw)
		}
		return port, nil
	}
	if u.Scheme == "http" {
		return 80, nil
	}
	return 443, nil
}

// buildClient returns the HTTP client used by the provider. The redirect
// policy is always the provider policy, never the one of a custom client.
func (p *OpenAIProvider) buildClient() *http.Client {
	client := &http.Client{
		Timeout:   defaultOpenAIClientTimeout,
		Transport: p.newTransport(nil),
	}
	if p.customClient != nil {
		client = &http.Client{
			Transport: p.customClient.Transport,
			Jar:       p.customClient.Jar,
			Timeout:   p.customClient.Timeout,
		}
		if client.Timeout <= 0 {
			client.Timeout = defaultOpenAIClientTimeout
		}
		switch transport := client.Transport.(type) {
		case nil:
			client.Transport = p.newTransport(nil)
		case *http.Transport:
			client.Transport = p.newTransport(transport)
		default:
			client.Transport = &openAIGuardedRoundTripper{base: transport, provider: p}
		}
	}
	client.CheckRedirect = p.checkRedirect
	return client
}

// newTransport returns a transport that always dials through the pinning
// dialer of the provider.
func (p *OpenAIProvider) newTransport(base *http.Transport) *http.Transport {
	var transport *http.Transport
	if base != nil {
		transport = base.Clone()
	} else {
		transport = &http.Transport{
			// No ambient proxy: a proxy would resolve the endpoint itself
			// and break the pinned address guarantee.
			Proxy:                 nil,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          10,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
	}
	// Always dial through the pinning dialer so the address that was
	// validated is the address that is connected to. DialContext takes
	// priority over the deprecated Dial hook, but a custom TLS dialer
	// would bypass the pinning and has to be removed.
	transport.DialContext = p.dialContext
	transport.DialTLS = nil //nolint:staticcheck // A custom TLS dialer would bypass the pinned address.
	transport.DialTLSContext = nil
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		if transport.TLSClientConfig.MinVersion == 0 {
			transport.TLSClientConfig.MinVersion = tls.VersionTLS12
		}
		// ServerName is left empty on purpose: net/http fills it with the
		// hostname of the request, so the certificate is still validated
		// against the original hostname even though the connection was made
		// to a pinned IP address.
		transport.TLSClientConfig.ServerName = ""
	}
	return transport
}

// dialContext validates the target host, resolves it, rejects reserved
// addresses and connects to the pinned address.
func (p *OpenAIProvider) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid target address %q", ErrOpenAIEndpointNotAllowed, addr)
	}
	normalized := normalizeOpenAIHost(host)
	if !p.policy.hostAllowed(normalized) {
		return nil, fmt.Errorf("%w: host %q is not on the endpoint allowlist", ErrOpenAIEndpointNotAllowed, normalized)
	}
	ips, err := p.resolveHost(ctx, normalized)
	if err != nil {
		return nil, err
	}
	if err := p.checkResolvedAddresses(normalized, ips); err != nil {
		return nil, err
	}
	// Connect to the address that was just validated. The TLS handshake
	// still uses the original hostname, so a swapped DNS answer cannot
	// hijack the request.
	var dialer net.Dialer
	var lastErr error
	for _, ip := range ips {
		target := net.JoinHostPort(ip.String(), port)
		conn, err := dialer.DialContext(ctx, network, target)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("openai dial failed: %w", lastErr)
}

// resolveHost resolves a host to the addresses that may be connected to.
func (p *OpenAIProvider) resolveHost(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	resolver := p.resolve
	if resolver == nil {
		resolver = defaultOpenAIResolver
	}
	ips, err := resolver(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("%w: dns lookup failed for host %q: %w", ErrOpenAIEndpointNotAllowed, host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%w: no addresses resolved for host %q", ErrOpenAIEndpointNotAllowed, host)
	}
	return ips, nil
}

// checkResolvedAddresses rejects hosts that resolve to a loopback,
// link-local, private or otherwise reserved address.
func (p *OpenAIProvider) checkResolvedAddresses(host string, ips []net.IP) error {
	if len(ips) == 0 {
		return fmt.Errorf("%w: no addresses resolved for host %q", ErrOpenAIEndpointNotAllowed, host)
	}
	if p.policy.reservedAllowed(host) {
		return nil
	}
	for _, ip := range ips {
		if isReservedOpenAIAddr(ip) {
			return fmt.Errorf("%w: host %q resolves to reserved address %s", ErrOpenAIReservedAddress, host, ip.String())
		}
	}
	return nil
}

// checkRedirect is the redirect policy of the provider. Redirects are refused
// by default so a redirect can never move the request (and the Authorization
// header) to a host outside the allowlist.
func (p *OpenAIProvider) checkRedirect(req *http.Request, via []*http.Request) error {
	if !p.policy.allowRedirect {
		return fmt.Errorf("%w: refusing redirect", ErrOpenAIRedirectBlocked)
	}
	if len(via) >= 10 {
		return fmt.Errorf("%w: too many redirects", ErrOpenAIRedirectBlocked)
	}
	if req == nil || req.URL == nil {
		return fmt.Errorf("%w: redirect without target url", ErrOpenAIRedirectBlocked)
	}
	// The redirect target is re-validated against the endpoint policy.
	if err := p.validateEndpoint(req.URL); err != nil {
		return fmt.Errorf("%w: %w", ErrOpenAIRedirectBlocked, err)
	}
	return nil
}

// openAIGuardedRoundTripper validates the endpoint of every request that is
// sent through a custom round tripper.
type openAIGuardedRoundTripper struct {
	base     http.RoundTripper
	provider *OpenAIProvider
}

// RoundTrip validates the request endpoint and delegates to the base round
// tripper.
func (g *openAIGuardedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, fmt.Errorf("%w: request without url", ErrOpenAIEndpointNotAllowed)
	}
	if err := g.provider.validateEndpoint(req.URL); err != nil {
		return nil, err
	}
	return g.base.RoundTrip(req)
}

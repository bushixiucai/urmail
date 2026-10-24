package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// newLocalTestProvider returns a provider that talks to a local httptest
// server. The local address needs the explicit reserved-host opt-in, so the
// test also proves that the opt-in is what makes the endpoint usable.
func newLocalTestProvider(t *testing.T, apiKey string, srv *httptest.Server, opts ...OpenAIOption) *OpenAIProvider {
	t.Helper()
	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("failed to parse server url: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("failed to parse server port: %v", err)
	}
	base := []OpenAIOption{
		WithOpenAIBaseURL(srv.URL + "/v1"),
		WithOpenAIAllowInsecureHTTP(),
		WithOpenAIAllowReservedHosts(parsed.Hostname()),
		WithOpenAIAllowedPorts(port),
	}
	all := make([]OpenAIOption, 0, len(base)+len(opts))
	all = append(all, base...)
	all = append(all, opts...)
	p, err := NewOpenAIProvider(apiKey, all...)
	if err != nil {
		t.Fatalf("NewOpenAIProvider failed: %v", err)
	}
	return p
}

// newPinnedTestProvider returns a provider for an allowlisted hostname whose
// resolution is pinned to the given addresses, so no test depends on DNS.
func newPinnedTestProvider(t *testing.T, apiKey, baseURL, host string, ips []net.IP, opts ...OpenAIOption) *OpenAIProvider {
	t.Helper()
	all := make([]OpenAIOption, 0, len(opts)+1)
	all = append(all, WithOpenAIBaseURL(baseURL))
	all = append(all, opts...)
	p, err := NewOpenAIProvider(apiKey, all...)
	if err != nil {
		t.Fatalf("NewOpenAIProvider failed: %v", err)
	}
	p.resolve = func(_ context.Context, resolved string) ([]net.IP, error) {
		if resolved != host {
			t.Errorf("unexpected host resolved: %s", resolved)
		}
		return ips, nil
	}
	return p
}

func TestNewOpenAIProvider_RejectsDisallowedEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		opts    []OpenAIOption
		wantErr error
	}{
		{
			name:    "host not on the allowlist",
			baseURL: "https://evil.example.com/v1",
			wantErr: ErrOpenAIEndpointNotAllowed,
		},
		{
			name:    "allowlist suffix confusion",
			baseURL: "https://api.openai.com.evil.example/v1",
			wantErr: ErrOpenAIEndpointNotAllowed,
		},
		{
			name:    "cloud metadata endpoint",
			baseURL: "https://169.254.169.254/latest/meta-data",
			wantErr: ErrOpenAIEndpointNotAllowed,
		},
		{
			name:    "loopback host",
			baseURL: "https://127.0.0.1/v1",
			wantErr: ErrOpenAIEndpointNotAllowed,
		},
		{
			name:    "localhost host",
			baseURL: "https://localhost/v1",
			wantErr: ErrOpenAIEndpointNotAllowed,
		},
		{
			name:    "plain http without opt-in",
			baseURL: "http://api.openai.com/v1",
			wantErr: ErrOpenAISchemeNotAllowed,
		},
		{
			name:    "plain http with port without opt-in",
			baseURL: "http://api.openai.com:8080/v1",
			wantErr: ErrOpenAISchemeNotAllowed,
		},
		{
			name:    "file scheme",
			baseURL: "file:///etc/passwd",
			wantErr: ErrOpenAISchemeNotAllowed,
		},
		{
			name:    "no scheme",
			baseURL: "api.openai.com/v1",
			wantErr: ErrOpenAISchemeNotAllowed,
		},
		{
			name:    "non standard port",
			baseURL: "https://api.openai.com:8443/v1",
			wantErr: ErrOpenAIPortNotAllowed,
		},
		{
			name:    "reserved ip literal on the allowlist",
			baseURL: "https://127.0.0.1:8443/v1",
			opts: []OpenAIOption{
				WithOpenAIAllowedHosts("127.0.0.1"),
				WithOpenAIAllowedPorts(8443),
			},
			wantErr: ErrOpenAIReservedAddress,
		},
		{
			name:    "userinfo in the url",
			baseURL: "https://user:secret@api.openai.com/v1",
			wantErr: ErrOpenAIEndpointNotAllowed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := make([]OpenAIOption, 0, len(tc.opts)+1)
			opts = append(opts, tc.opts...)
			opts = append(opts, WithOpenAIBaseURL(tc.baseURL))
			p, err := NewOpenAIProvider("test-key", opts...)
			if err == nil {
				t.Fatalf("expected error for base url %s, got provider %v", tc.baseURL, p)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("expected error %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestNewOpenAIProvider_AcceptsAllowlistedEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		opts    []OpenAIOption
	}{
		{
			name:    "official api host",
			baseURL: "https://api.openai.com/v1",
		},
		{
			name:    "official api host without path",
			baseURL: "https://api.openai.com",
		},
		{
			name:    "official api host with explicit standard port",
			baseURL: "https://api.openai.com:443/v1",
		},
		{
			name:    "operator added host",
			baseURL: "https://llm.example.com/v1",
			opts: []OpenAIOption{
				WithOpenAIAllowedHosts("llm.example.com"),
			},
		},
		{
			name:    "operator added host with non standard port",
			baseURL: "https://llm.example.com:8443/v1",
			opts: []OpenAIOption{
				WithOpenAIAllowedHosts("llm.example.com"),
				WithOpenAIAllowedPorts(8443),
			},
		},
		{
			name:    "plain http with the explicit opt-in",
			baseURL: "http://api.openai.com/v1",
			opts: []OpenAIOption{
				WithOpenAIAllowInsecureHTTP(),
			},
		},
		{
			name:    "reserved host with the explicit opt-in",
			baseURL: "https://127.0.0.1:8443/v1",
			opts: []OpenAIOption{
				WithOpenAIAllowReservedHosts("127.0.0.1"),
				WithOpenAIAllowedPorts(8443),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := make([]OpenAIOption, 0, len(tc.opts)+1)
			opts = append(opts, tc.opts...)
			opts = append(opts, WithOpenAIBaseURL(tc.baseURL))
			if _, err := NewOpenAIProvider("test-key", opts...); err != nil {
				t.Fatalf("expected base url %s to be accepted, got %v", tc.baseURL, err)
			}
		})
	}
}

func TestNewOpenAIProvider_DefaultEndpointIsAllowed(t *testing.T) {
	p, err := NewOpenAIProvider("test-key")
	if err != nil {
		t.Fatalf("expected the default endpoint to be accepted, got %v", err)
	}
	if p.Name() != ProviderOpenAI {
		t.Errorf("expected name %s, got %s", ProviderOpenAI, p.Name())
	}
	if p.baseURL != defaultOpenAIBaseURL {
		t.Errorf("expected base url %s, got %s", defaultOpenAIBaseURL, p.baseURL)
	}
	if !p.policy.hostAllowed("api.openai.com") {
		t.Error("the official api host must be on the default allowlist")
	}
	if p.policy.hostAllowed("evil.example.com") {
		t.Error("an unknown host must not be on the default allowlist")
	}
}

func TestOpenAIProvider_Complete_Success(t *testing.T) {
	apiKey := "test-openai-api-key"
	expectedContent := "This is a helpful AI response."

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("expected path /v1/chat/completions, got %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer "+apiKey {
			t.Errorf("expected auth Bearer %s, got %s", apiKey, auth)
		}

		var reqBody struct {
			Model       string    `json:"model"`
			Messages    []Message `json:"messages"`
			Temperature *float64  `json:"temperature"`
			MaxTokens   *int      `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		}
		if reqBody.Model != "gpt-4o" {
			t.Errorf("expected model gpt-4o, got %s", reqBody.Model)
		}
		if len(reqBody.Messages) != 2 {
			t.Errorf("expected 2 messages, got %d", len(reqBody.Messages))
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-test12345",
			"object": "chat.completion",
			"created": 1700000000,
			"choices": [
				{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "` + expectedContent + `"
					},
					"finish_reason": "stop"
				}
			],
			"usage": {
				"prompt_tokens": 15,
				"completion_tokens": 10,
				"total_tokens": 25
			}
		}`))
	}))
	defer srv.Close()

	p := newLocalTestProvider(t, apiKey, srv,
		WithOpenAIDefaultModel("gpt-4o-mini"),
		WithOpenAIName("my-openai"),
	)

	if p.Name() != "my-openai" {
		t.Errorf("expected name my-openai, got %s", p.Name())
	}

	temp := 0.7
	maxTokens := 100
	resp, err := p.Complete(context.Background(), CompletionRequest{
		Model: "gpt-4o",
		Messages: []Message{
			{Role: "system", Content: "You are a test assistant."},
			{Role: "user", Content: "Hello world!"},
		},
		Temperature: &temp,
		MaxTokens:   &maxTokens,
	})
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}

	if resp.Content != expectedContent {
		t.Errorf("expected content %q, got %q", expectedContent, resp.Content)
	}
	if resp.Text != expectedContent {
		t.Errorf("expected text %q, got %q", expectedContent, resp.Text)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("expected finish_reason stop, got %s", resp.FinishReason)
	}
	if resp.PromptTokens != 15 || resp.OutputTokens != 10 || resp.TotalTokens != 25 {
		t.Errorf("unexpected usage counts: %+v", resp)
	}
}

// TestOpenAIProvider_AllowlistedHostWithValidAddressIsAccepted proves that an
// allowlisted host whose pinned address is valid is contacted successfully.
func TestOpenAIProvider_AllowlistedHostWithValidAddressIsAccepted(t *testing.T) {
	apiKey := "test-openai-api-key"
	var gotHost, gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pinned answer"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("failed to parse server url: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("failed to parse server port: %v", err)
	}

	p := newPinnedTestProvider(t, apiKey,
		"http://llm.example.test:"+strconv.Itoa(port)+"/v1",
		"llm.example.test",
		[]net.IP{net.ParseIP("127.0.0.1")},
		WithOpenAIAllowInsecureHTTP(),
		WithOpenAIAllowedHosts("llm.example.test"),
		WithOpenAIAllowReservedHosts("llm.example.test"),
		WithOpenAIAllowedPorts(port),
	)

	resp, err := p.Complete(context.Background(), CompletionRequest{Prompt: "hello"})
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	if resp.Content != "pinned answer" {
		t.Errorf("unexpected content: %q", resp.Content)
	}
	if gotHost != "llm.example.test:"+strconv.Itoa(port) {
		t.Errorf("expected the original hostname in the host header, got %s", gotHost)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("expected path /v1/chat/completions, got %s", gotPath)
	}
	if gotAuth != "Bearer "+apiKey {
		t.Errorf("expected auth Bearer %s, got %s", apiKey, gotAuth)
	}
}

// TestOpenAIProvider_RejectsReservedResolvedAddress proves that an
// allowlisted host that resolves to a reserved address is refused before the
// connection is made.
func TestOpenAIProvider_RejectsReservedResolvedAddress(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"should never be reached"}}]}`))
	}))
	defer srv.Close()

	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("failed to parse server url: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("failed to parse server port: %v", err)
	}

	for _, reserved := range []string{"127.0.0.1", "169.254.169.254", "10.0.0.5", "192.168.1.1"} {
		t.Run(reserved, func(t *testing.T) {
			p := newPinnedTestProvider(t, "test-key",
				"http://llm.example.test:"+strconv.Itoa(port)+"/v1",
				"llm.example.test",
				[]net.IP{net.ParseIP(reserved)},
				WithOpenAIAllowInsecureHTTP(),
				WithOpenAIAllowedHosts("llm.example.test"),
				WithOpenAIAllowedPorts(port),
			)

			_, err := p.Complete(context.Background(), CompletionRequest{Prompt: "hello"})
			if err == nil {
				t.Fatal("expected the reserved address to be refused")
			}
			if !errors.Is(err, ErrOpenAIReservedAddress) {
				t.Errorf("expected ErrOpenAIReservedAddress, got %v", err)
			}
		})
	}

	if hits.Load() != 0 {
		t.Errorf("the reserved address was contacted %d times", hits.Load())
	}
}

// TestOpenAIProvider_ReservedHostOptIn proves that the explicit operator
// opt-in allows an on-premise gateway on a reserved address.
func TestOpenAIProvider_ReservedHostOptIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"on-premise answer"}}]}`))
	}))
	defer srv.Close()

	p := newLocalTestProvider(t, "test-key", srv)
	resp, err := p.Complete(context.Background(), CompletionRequest{Prompt: "hello"})
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	if resp.Content != "on-premise answer" {
		t.Errorf("unexpected content: %q", resp.Content)
	}
}

// TestOpenAIProvider_RefusesRedirect proves that an upstream redirect is
// refused and that the redirect target is never contacted.
func TestOpenAIProvider_RefusesRedirect(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"redirected"}}]}`))
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/chat/completions", http.StatusFound)
	}))
	defer source.Close()

	parsed, err := url.Parse(source.URL)
	if err != nil {
		t.Fatalf("failed to parse server url: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("failed to parse server port: %v", err)
	}

	p := newPinnedTestProvider(t, "test-key",
		"http://llm.example.test:"+strconv.Itoa(port)+"/v1",
		"llm.example.test",
		[]net.IP{net.ParseIP("127.0.0.1")},
		WithOpenAIAllowInsecureHTTP(),
		WithOpenAIAllowedHosts("llm.example.test"),
		WithOpenAIAllowReservedHosts("llm.example.test"),
		WithOpenAIAllowedPorts(port),
	)

	_, err = p.Complete(context.Background(), CompletionRequest{Prompt: "hello"})
	if err == nil {
		t.Fatal("expected the redirect to be refused")
	}
	if !errors.Is(err, ErrOpenAIRedirectBlocked) {
		t.Errorf("expected ErrOpenAIRedirectBlocked, got %v", err)
	}
	if targetHits.Load() != 0 {
		t.Errorf("the redirect target was contacted %d times", targetHits.Load())
	}
}

// TestOpenAIProvider_RefusesRedirectToDisallowedHost proves that even with
// the redirect opt-in a redirect to a host outside the allowlist is refused.
func TestOpenAIProvider_RefusesRedirectToDisallowedHost(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"redirected"}}]}`))
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/chat/completions", http.StatusFound)
	}))
	defer source.Close()

	parsed, err := url.Parse(source.URL)
	if err != nil {
		t.Fatalf("failed to parse server url: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("failed to parse server port: %v", err)
	}

	p := newPinnedTestProvider(t, "test-key",
		"http://llm.example.test:"+strconv.Itoa(port)+"/v1",
		"llm.example.test",
		[]net.IP{net.ParseIP("127.0.0.1")},
		WithOpenAIAllowInsecureHTTP(),
		WithOpenAIAllowedHosts("llm.example.test"),
		WithOpenAIAllowReservedHosts("llm.example.test"),
		WithOpenAIAllowedPorts(port),
		WithOpenAIAllowRedirects(),
	)

	_, err = p.Complete(context.Background(), CompletionRequest{Prompt: "hello"})
	if err == nil {
		t.Fatal("expected the redirect to be refused")
	}
	if !errors.Is(err, ErrOpenAIRedirectBlocked) {
		t.Errorf("expected ErrOpenAIRedirectBlocked, got %v", err)
	}
	if targetHits.Load() != 0 {
		t.Errorf("the redirect target was contacted %d times", targetHits.Load())
	}
}

// TestOpenAIProvider_CustomClientCannotDisableRedirectPolicy proves that a
// custom HTTP client cannot re-enable redirect following.
func TestOpenAIProvider_CustomClientCannotDisableRedirectPolicy(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"redirected"}}]}`))
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/chat/completions", http.StatusFound)
	}))
	defer source.Close()

	parsed, err := url.Parse(source.URL)
	if err != nil {
		t.Fatalf("failed to parse server url: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("failed to parse server port: %v", err)
	}

	custom := &http.Client{
		// A permissive redirect policy must not be able to override the
		// provider policy.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return nil
		},
	}

	p := newPinnedTestProvider(t, "test-key",
		"http://llm.example.test:"+strconv.Itoa(port)+"/v1",
		"llm.example.test",
		[]net.IP{net.ParseIP("127.0.0.1")},
		WithOpenAIAllowInsecureHTTP(),
		WithOpenAIAllowedHosts("llm.example.test"),
		WithOpenAIAllowReservedHosts("llm.example.test"),
		WithOpenAIAllowedPorts(port),
		WithOpenAIHTTPClient(custom),
	)

	_, err = p.Complete(context.Background(), CompletionRequest{Prompt: "hello"})
	if err == nil {
		t.Fatal("expected the redirect to be refused")
	}
	if !errors.Is(err, ErrOpenAIRedirectBlocked) {
		t.Errorf("expected ErrOpenAIRedirectBlocked, got %v", err)
	}
	if targetHits.Load() != 0 {
		t.Errorf("the redirect target was contacted %d times", targetHits.Load())
	}
}

// TestOpenAIProvider_ErrorDoesNotLeakAPIKey proves that neither the API key
// nor the request body ever shows up in an error message.
func TestOpenAIProvider_ErrorDoesNotLeakAPIKey(t *testing.T) {
	apiKey := "sk-super-secret-api-key-0123456789"
	secretPrompt := "prompt-with-secret-content-9f3a"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A hostile upstream echoes the credentials back at us.
		body := "invalid api key: " + apiKey + " for prompt " + secretPrompt
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	p := newLocalTestProvider(t, apiKey, srv)

	_, err := p.Complete(context.Background(), CompletionRequest{Prompt: secretPrompt})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
	msg := err.Error()
	if strings.Contains(msg, apiKey) {
		t.Errorf("error message leaks the api key: %s", msg)
	}
	if strings.Contains(msg, secretPrompt) {
		t.Errorf("error message leaks the request body: %s", msg)
	}
}

// TestOpenAIProvider_TransportErrorDoesNotLeakAPIKey proves that a transport
// failure (here: a refused redirect) does not leak the API key either.
func TestOpenAIProvider_TransportErrorDoesNotLeakAPIKey(t *testing.T) {
	apiKey := "sk-super-secret-api-key-0123456789"

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"redirected"}}]}`))
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/chat/completions", http.StatusFound)
	}))
	defer source.Close()

	parsed, err := url.Parse(source.URL)
	if err != nil {
		t.Fatalf("failed to parse server url: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("failed to parse server port: %v", err)
	}

	p := newPinnedTestProvider(t, apiKey,
		"http://llm.example.test:"+strconv.Itoa(port)+"/v1",
		"llm.example.test",
		[]net.IP{net.ParseIP("127.0.0.1")},
		WithOpenAIAllowInsecureHTTP(),
		WithOpenAIAllowedHosts("llm.example.test"),
		WithOpenAIAllowReservedHosts("llm.example.test"),
		WithOpenAIAllowedPorts(port),
	)

	_, err = p.Complete(context.Background(), CompletionRequest{Prompt: "hello"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Errorf("error message leaks the api key: %s", err.Error())
	}
}

func TestOpenAIProvider_Complete_EmptyPrompt(t *testing.T) {
	p, err := NewOpenAIProvider("test-key")
	if err != nil {
		t.Fatalf("NewOpenAIProvider failed: %v", err)
	}
	_, err = p.Complete(context.Background(), CompletionRequest{})
	if !errors.Is(err, ErrEmptyPrompt) {
		t.Errorf("expected ErrEmptyPrompt, got %v", err)
	}
}

func TestOpenAIProvider_ErrorStatuses(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		expectedErr error
	}{
		{
			name:        "Unauthorized 401",
			status:      http.StatusUnauthorized,
			body:        `{"error": {"message": "invalid api key"}}`,
			expectedErr: ErrUnauthorized,
		},
		{
			name:        "Rate Limit 429",
			status:      http.StatusTooManyRequests,
			body:        `{"error": {"message": "rate limit exceeded"}}`,
			expectedErr: ErrRateLimitExceeded,
		},
		{
			name:        "Internal Server Error 500",
			status:      http.StatusInternalServerError,
			body:        `{"error": {"message": "internal error"}}`,
			expectedErr: ErrProviderError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			p := newLocalTestProvider(t, "key", srv)
			_, err := p.Complete(context.Background(), CompletionRequest{Prompt: "test"})
			if !errors.Is(err, tc.expectedErr) {
				t.Errorf("expected error %v, got %v", tc.expectedErr, err)
			}
		})
	}
}

func TestOpenAIProvider_InvalidResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": []}`))
	}))
	defer srv.Close()

	p := newLocalTestProvider(t, "key", srv)
	_, err := p.Complete(context.Background(), CompletionRequest{Prompt: "hi"})
	if !errors.Is(err, ErrInvalidResponse) {
		t.Errorf("expected ErrInvalidResponse for empty choices, got %v", err)
	}
}

func TestOpenAIProvider_JSONMode(t *testing.T) {
	var receivedJSONMode bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ResponseFormat *struct {
				Type string `json:"type"`
			} `json:"response_format"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.ResponseFormat != nil && body.ResponseFormat.Type == "json_object" {
			receivedJSONMode = true
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer srv.Close()

	p := newLocalTestProvider(t, "key", srv)
	_, err := p.Complete(context.Background(), CompletionRequest{
		Prompt:   "give json",
		JSONMode: true,
	})
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	if !receivedJSONMode {
		t.Error("expected json_object response_format to be set in request")
	}
}

func TestOpenAIProvider_ErrorBodyNotEchoedVerbatim(t *testing.T) {
	secret := strings.Repeat("TOPSECRET-PAYLOAD-", 600) // ~10KB body
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(secret + "\n\tctrl\x00chars"))
	}))
	defer srv.Close()

	p := newLocalTestProvider(t, "key", srv)
	_, err := p.Complete(context.Background(), CompletionRequest{Prompt: "hi"})
	if !errors.Is(err, ErrProviderError) {
		t.Fatalf("expected ErrProviderError, got %v", err)
	}
	msg := err.Error()
	// The tail of the body (with its control characters) must not survive.
	if strings.Contains(msg, "ctrl") || strings.Contains(msg, strings.Repeat("TOPSECRET-PAYLOAD-", 100)) {
		t.Errorf("error message echoes the untruncated upstream body")
	}
	if strings.Count(msg, "TOPSECRET-PAYLOAD-") > 16 {
		t.Errorf("upstream body not truncated: len=%d", len(msg))
	}
	if len(msg) > 400 {
		t.Errorf("error message too long (%d bytes): must summarize, not echo body", len(msg))
	}
	if !strings.Contains(msg, "status 500") {
		t.Errorf("error message should include the status code: %s", msg)
	}
	if strings.ContainsAny(msg, "\x00\n\t") {
		t.Errorf("error message contains raw control characters: %q", msg)
	}
}

func TestIsReservedOpenAIAddr(t *testing.T) {
	reserved := []string{
		"127.0.0.1",
		"127.1.2.3",
		"::1",
		"0.0.0.0",
		"0.1.2.3",
		"10.0.0.5",
		"172.16.0.1",
		"192.168.1.1",
		"169.254.169.254",
		"100.64.0.1",
		"192.0.0.1",
		"198.18.0.1",
		"255.255.255.255",
		"::ffff:127.0.0.1",
		"fd00::1",
		"fe80::1",
		"224.0.0.1",
	}
	for _, addr := range reserved {
		t.Run("reserved_"+addr, func(t *testing.T) {
			if !isReservedOpenAIAddr(net.ParseIP(addr)) {
				t.Errorf("expected %s to be reserved", addr)
			}
		})
	}

	public := []string{
		"93.184.216.34",
		"8.8.8.8",
		"172.32.0.1",
		"2606:2800:220:1:248:1893:25c8:1946",
	}
	for _, addr := range public {
		t.Run("public_"+addr, func(t *testing.T) {
			if isReservedOpenAIAddr(net.ParseIP(addr)) {
				t.Errorf("expected %s to be allowed", addr)
			}
		})
	}
}

func TestNormalizeOpenAIHost(t *testing.T) {
	tests := map[string]string{
		"API.OpenAI.com":      "api.openai.com",
		"api.openai.com.":     "api.openai.com",
		"  llm.example.com  ": "llm.example.com",
		"[::1]":               "::1",
		"127.0.0.1":           "127.0.0.1",
		"::ffff:127.0.0.1":    "127.0.0.1",
	}
	for input, want := range tests {
		if got := normalizeOpenAIHost(input); got != want {
			t.Errorf("normalizeOpenAIHost(%q) = %q, want %q", input, got, want)
		}
	}
}

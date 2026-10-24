package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestOllamaRedirectCannotEscapeApprovedEndpoint(t *testing.T) {
	var escaped atomic.Int64
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { escaped.Add(1); w.WriteHeader(http.StatusOK) }))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/api/chat", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	provider, err := NewOllamaProvider(WithOllamaBaseURL(origin.URL), WithOllamaHTTPClient(origin.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Complete(t.Context(), CompletionRequest{Prompt: "Private selected message"}); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("redirect policy not enforced: %v", err)
	}
	if escaped.Load() != 0 {
		t.Fatal("model message escaped to another local service")
	}
	for _, endpoint := range []string{"http://user:password@localhost:11434", "ftp://localhost:11434", "http://localhost:11434?redirect=metadata"} {
		if validateLocalEndpoint(endpoint) == nil {
			t.Fatalf("unsafe local endpoint accepted %s", endpoint)
		}
	}
}

func TestProviderHealthDoesNotGenerateOrSendMessage(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("health performed a completion: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	provider, err := ConfiguredProvider("ollama", server.URL, "model", "", nil, []string{server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err = CheckHealth(context.Background(), provider); err != nil || calls.Load() != 1 {
		t.Fatalf("metadata health %v", err)
	}
}

func TestAzureV1AdapterUsesDeploymentAndAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/chat/completions" || r.Header.Get("api-key") != "test-key" || r.Header.Get("Authorization") != "" {
			t.Errorf("wrong Azure request %s", r.URL.Path)
		}
		var input struct {
			Model string `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Model != "deployment-name" {
			t.Error("deployment not preserved")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"summary\":\"safe\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(parsed.Port())
	provider, err := NewAzureProvider("test-key", WithOpenAIBaseURL(server.URL+"/openai/v1"), WithOpenAIDefaultModel("deployment-name"), WithOpenAIAllowReservedHosts(parsed.Hostname()), WithOpenAIAllowedPorts(port), WithOpenAIAllowInsecureHTTP())
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Complete(t.Context(), CompletionRequest{Prompt: "Offline fixture"})
	if err != nil || !result.UsageKnown || result.TotalTokens != 15 {
		t.Fatalf("Azure response %v %+v", err, result)
	}
}

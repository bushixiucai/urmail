package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestFetchModelsOpenAIFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("expected /v1/models, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Bearer test-key, got %s", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-4o"},{"id":"deepseek-chat"},{"id":"gpt-4o-mini"}]}`))
	}))
	defer server.Close()

	provider := newLocalTestProvider(t, "test-key", server)

	models, err := FetchModels(context.Background(), provider)
	if err != nil {
		t.Fatalf("fetch models failed: %v", err)
	}

	expected := []string{"deepseek-chat", "gpt-4o", "gpt-4o-mini"}
	if !reflect.DeepEqual(models, expected) {
		t.Errorf("expected %v, got %v", expected, models)
	}
}

func TestFetchModelsOllamaFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"llama3:latest","model":"llama3:latest"},{"name":"qwen2.5:7b","model":"qwen2.5:7b"}]}`))
	}))
	defer server.Close()

	provider, err := ConfiguredProvider("ollama", server.URL, "llama3", "", nil, []string{server.URL})
	if err != nil {
		t.Fatal(err)
	}

	models, err := FetchModels(context.Background(), provider)
	if err != nil {
		t.Fatalf("fetch models failed: %v", err)
	}

	expected := []string{"llama3:latest", "qwen2.5:7b"}
	if !reflect.DeepEqual(models, expected) {
		t.Errorf("expected %v, got %v", expected, models)
	}
}

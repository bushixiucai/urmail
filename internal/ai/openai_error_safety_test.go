package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCompletionErrorBodiesNeverExposeMailFragments(t *testing.T) {
	const apiKey = "sk-private-request-key"
	prompt := "PRIVATE-MAIL-FRAGMENT-" + strings.Repeat("confidential mail\n", 80)
	for _, variant := range []struct {
		name   string
		status int
		body   string
		kind   error
	}{
		{"long echo", 500, prompt, ErrProviderError},
		{"prefix echo", 401, prompt[:45], ErrUnauthorized},
		{"escaped echo", 429, fmt.Sprintf("{\"error\":%q}", prompt), ErrRateLimitExceeded},
		{"rewritten echo", 502, strings.ToUpper(strings.Join(strings.Fields(prompt), " ")), ErrProviderError},
		{"credential fragment", 403, apiKey[3:15], ErrUnauthorized},
		{"success error envelope", 200, fmt.Sprintf(`{"error":{"message":%q},"choices":[{"message":{"content":"ignore"}}]}`, prompt), ErrProviderError},
		{"malformed response", 200, "PRIVATE-MAIL-FRAGMENT-not-json", ErrInvalidResponse},
	} {
		t.Run(variant.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(variant.status)
				_, _ = io.WriteString(w, variant.body)
			}))
			defer server.Close()
			provider := newLocalTestProvider(t, apiKey, server)
			_, err := provider.Complete(context.Background(), CompletionRequest{Prompt: prompt})
			if !errors.Is(err, variant.kind) {
				t.Fatalf("expected safe classification %v, got %v", variant.kind, err)
			}
			for _, fragment := range []string{"PRIVATE", "confidential", "CONFIDENTIAL", "private-request", "request-key"} {
				if strings.Contains(err.Error(), fragment) {
					t.Fatalf("error exposes request content: %q", err.Error())
				}
			}
			if len(err.Error()) > 120 {
				t.Fatalf("error contains unexpected upstream details: %q", err.Error())
			}
		})
	}
}

type completionRoundTripper func(*http.Request) (*http.Response, error)

func (f completionRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type failingCompletionBody struct{}

func (failingCompletionBody) Read([]byte) (int, error) {
	return 0, errors.New("PRIVATE-MAIL-FRAGMENT in proxy reader")
}

func (failingCompletionBody) Close() error { return nil }

func TestCompletionTransportAndReaderErrorsAreSafe(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	for _, variant := range []struct {
		name      string
		transport completionRoundTripper
		kind      error
	}{
		{"transport", func(*http.Request) (*http.Response, error) {
			return nil, errors.New("PRIVATE-MAIL-FRAGMENT in proxy transport")
		}, ErrProviderError},
		{"reader", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: failingCompletionBody{}, Header: make(http.Header)}, nil
		}, ErrInvalidResponse},
	} {
		t.Run(variant.name, func(t *testing.T) {
			provider := newLocalTestProvider(t, "key", server)
			provider.client.Transport = variant.transport
			_, err := provider.Complete(context.Background(), CompletionRequest{Prompt: "private request"})
			if !errors.Is(err, variant.kind) || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("unsafe or unclassified error: %v", err)
			}
		})
	}
}

func TestCompletionCancellationClassificationsSurvive(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	for _, want := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(want.Error(), func(t *testing.T) {
			provider := newLocalTestProvider(t, "key", server)
			provider.client.Transport = completionRoundTripper(func(*http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("PRIVATE-MAIL-FRAGMENT: %w", want)
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := provider.Complete(ctx, CompletionRequest{Prompt: "private request"})
			if !errors.Is(err, want) || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("cancellation classification lost or leaked: %v", err)
			}
		})
	}
}

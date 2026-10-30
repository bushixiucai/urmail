package stub

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bushixiucai/urmail/internal/ai"
)

// RecordedRequest stores metadata and decoded payload of one HTTP request.
type RecordedRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Body    []byte
	JSON    map[string]any
}

// ModelStub is an in-memory HTTP model stub that captures all interactions.
type ModelStub struct {
	Server       *httptest.Server
	mu           sync.Mutex
	requests     []RecordedRequest
	statusCode   int
	responseBody string
	contentType  string
	delay        time.Duration
	handler      http.HandlerFunc
}

// NewModelStub starts a mock model endpoint.
func NewModelStub() *ModelStub {
	s := &ModelStub{
		statusCode:   http.StatusOK,
		contentType:  "application/json",
		responseBody: `{"object":"list","data":[{"id":"gpt-4o"},{"id":"deepseek-chat"}]}`,
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()

		body, _ := io.ReadAll(r.Body)
		var parsedJSON map[string]any
		_ = json.Unmarshal(body, &parsedJSON)

		rec := RecordedRequest{
			Method:  r.Method,
			Path:    r.URL.Path,
			Headers: r.Header.Clone(),
			Body:    body,
			JSON:    parsedJSON,
		}
		s.requests = append(s.requests, rec)

		if s.delay > 0 {
			time.Sleep(s.delay)
		}

		if s.handler != nil {
			s.handler(w, r)
			return
		}

		w.Header().Set("Content-Type", s.contentType)
		w.WriteHeader(s.statusCode)
		_, _ = w.Write([]byte(s.responseBody))
	}))
	return s
}

// Close stops the server.
func (s *ModelStub) Close() {
	if s.Server != nil {
		s.Server.Close()
	}
}

// URL returns the base URL of the stub.
func (s *ModelStub) URL() string {
	return s.Server.URL
}

// SetResponse configures a canned response.
func (s *ModelStub) SetResponse(status int, contentType, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusCode = status
	s.contentType = contentType
	s.responseBody = body
	s.handler = nil
}

// SetCustomHandler allows dynamic response logic.
func (s *ModelStub) SetCustomHandler(h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = h
}

// Reset clears recorded requests and resets response to default.
func (s *ModelStub) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
	s.statusCode = http.StatusOK
	s.contentType = "application/json"
	s.responseBody = `{"object":"list","data":[{"id":"gpt-4o"},{"id":"deepseek-chat"}]}`
	s.delay = 0
	s.handler = nil
}

// Requests returns a copy of all recorded requests.
func (s *ModelStub) Requests() []RecordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RecordedRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

// LastRequest returns the latest recorded request, or nil if none.
func (s *ModelStub) LastRequest() *RecordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		return nil
	}
	last := s.requests[len(s.requests)-1]
	return &last
}

// SetCompletionResponse sets a valid OpenAI chat completion response with JSON content.
func (s *ModelStub) SetCompletionResponse(jsonContent string) {
	escaped, _ := json.Marshal(jsonContent)
	payload := `{"id":"chatcmpl-test","object":"chat.completion","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":` + string(escaped) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`
	s.SetResponse(http.StatusOK, "application/json", payload)
}

// SetMalformedHTMLResponse simulates a proxy or captive portal returning HTML with HTTP 200.
func (s *ModelStub) SetMalformedHTMLResponse() {
	s.SetResponse(http.StatusOK, "text/html", "<!DOCTYPE html><html><body><h1>Service Notice</h1><p>Ok</p></body></html>")
}

// SetRateLimitResponse sets an HTTP 429 response.
func (s *ModelStub) SetRateLimitResponse() {
	s.SetResponse(http.StatusTooManyRequests, "application/json", `{"error":{"message":"Rate limit reached","type":"rate_limit_exceeded"}}`)
}

// SetUnauthorizedResponse sets an HTTP 401 response.
func (s *ModelStub) SetUnauthorizedResponse() {
	s.SetResponse(http.StatusUnauthorized, "application/json", `{"error":{"message":"Invalid API key","type":"invalid_request_error"}}`)
}

// SetServerErrorResponse sets an HTTP 500 response.
func (s *ModelStub) SetServerErrorResponse() {
	s.SetResponse(http.StatusInternalServerError, "application/json", `{"error":{"message":"Internal server error","type":"api_error"}}`)
}

// LastBodyContains reports whether the last request body contains a substring.
func (s *ModelStub) LastBodyContains(sub string) bool {
	req := s.LastRequest()
	if req == nil {
		return false
	}
	return strings.Contains(string(req.Body), sub)
}

// Provider constructs a valid test OpenAI Provider talking to this stub.
func (s *ModelStub) Provider(apiKey, model string) (ai.Provider, error) {
	parsed, err := url.Parse(s.Server.URL)
	if err != nil {
		return nil, err
	}
	port, _ := strconv.Atoi(parsed.Port())
	opts := []ai.OpenAIOption{
		ai.WithOpenAIBaseURL(s.Server.URL + "/v1"),
		ai.WithOpenAIDefaultModel(model),
		ai.WithOpenAIAllowInsecureHTTP(),
		ai.WithOpenAIAllowReservedHosts(parsed.Hostname()),
	}
	if port > 0 {
		opts = append(opts, ai.WithOpenAIAllowedPorts(port))
	}
	return ai.NewOpenAIProvider(apiKey, opts...)
}

package ai

import (
	"context"
	"io"
	"net/http"
	"strings"
)

// CheckHealth probes only the model catalog, never a billable completion or mail
// body. It reuses the exact adapter transport and endpoint security policy.
func CheckHealth(ctx context.Context, provider Provider) error {
	var endpoint, key string
	var client *http.Client
	azure := false
	switch p := provider.(type) {
	case *OpenAIProvider:
		var err error
		endpoint, err = p.buildEndpoint()
		if err != nil {
			return ErrProviderDisabled
		}
		client = p.client
		key = p.apiKey
		azure = p.azureAPIKey
	case *OllamaProvider:
		if validateLocalEndpoint(p.baseURL) != nil {
			return ErrProviderDisabled
		}
		endpoint = strings.TrimRight(p.baseURL, "/")
		if !strings.HasSuffix(endpoint, "/v1") {
			endpoint += "/v1"
		}
		endpoint += "/chat/completions"
		client = p.client
	default:
		return ErrProviderDisabled
	}
	endpoint = strings.TrimSuffix(endpoint, "/chat/completions") + "/models"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ErrInvalidRequest
	}
	if key != "" {
		if azure {
			request.Header.Set("api-key", key)
		} else {
			request.Header.Set("Authorization", "Bearer "+key)
		}
	}
	response, err := client.Do(request)
	if err != nil {
		return ErrProviderError
	}
	defer func() { _ = response.Body.Close() }()
	if _, err = io.Copy(io.Discard, io.LimitReader(response.Body, 4096)); err != nil {
		return ErrProviderError
	}
	if response.StatusCode != http.StatusOK {
		return ErrProviderError
	}
	return nil
}

package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
)

// FetchModels probes the model catalog of the provider and extracts available model IDs.
// It queries metadata-only endpoints (/models or /api/tags), never a billable completion.
func FetchModels(ctx context.Context, provider Provider) ([]string, error) {
	var endpoint, key string
	var client *http.Client
	azure := false
	isOllama := false
	switch p := provider.(type) {
	case *OpenAIProvider:
		var err error
		endpoint, err = p.buildEndpoint()
		if err != nil {
			return nil, ErrProviderDisabled
		}
		client = p.client
		key = p.apiKey
		azure = p.azureAPIKey
	case *OllamaProvider:
		if validateLocalEndpoint(p.baseURL) != nil {
			return nil, ErrProviderDisabled
		}
		endpoint = strings.TrimRight(p.baseURL, "/")
		client = p.client
		isOllama = true
	default:
		return nil, ErrProviderDisabled
	}

	if isOllama {
		if !strings.HasSuffix(endpoint, "/v1") {
			endpoint += "/v1"
		}
		endpoint += "/chat/completions"
	}
	endpoint = strings.TrimSuffix(endpoint, "/chat/completions") + "/models"

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, ErrInvalidRequest
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
		return nil, ErrProviderError
	}
	defer func() { _ = response.Body.Close() }()

	bodyBytes, err := io.ReadAll(io.LimitReader(response.Body, 512*1024))
	if err != nil {
		return nil, ErrProviderError
	}
	if response.StatusCode != http.StatusOK {
		return nil, ErrProviderError
	}

	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	_ = json.Unmarshal(bodyBytes, &parsed)

	seen := make(map[string]bool)
	var models []string
	for _, item := range parsed.Data {
		name := strings.TrimSpace(item.ID)
		if name != "" && !seen[name] {
			seen[name] = true
			models = append(models, name)
		}
	}
	for _, item := range parsed.Models {
		name := strings.TrimSpace(item.Model)
		if name == "" {
			name = strings.TrimSpace(item.Name)
		}
		if name != "" && !seen[name] {
			seen[name] = true
			models = append(models, name)
		}
	}
	sort.Strings(models)
	return models, nil
}

// CheckHealth probes only the model catalog, never a billable completion or mail body.
// It reuses the exact adapter transport and endpoint security policy.
func CheckHealth(ctx context.Context, provider Provider) error {
	_, err := FetchModels(ctx, provider)
	return err
}

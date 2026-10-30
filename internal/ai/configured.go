package ai

import (
	"net/url"
	"strconv"
	"strings"
)

// ConfiguredProvider enforces exact operator endpoints before adapter policy.
// Tenant configuration cannot expand cloud hosts or permitted local services.
func ConfiguredProvider(kind, endpoint, model, key string, cloudAllowed, localAllowed []string) (Provider, error) {
	allowed := cloudAllowed
	if kind == ProviderOllama {
		allowed = localAllowed
	}
	found := false
	for _, candidate := range allowed {
		if strings.TrimRight(candidate, "/") == strings.TrimRight(endpoint, "/") {
			found = true
			break
		}
	}
	if !found {
		return nil, ErrProviderDisabled
	}
	return DynamicProvider(kind, endpoint, model, key)
}

// DynamicProvider builds a validated provider instance for direct or user-provided (BYOK) endpoints.
// For cloud endpoints, HTTPS is strictly enforced and SSRF protections apply.
func DynamicProvider(kind, endpoint, model, key string) (Provider, error) {
	if kind == "" {
		kind = ProviderOpenAI
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, ErrInvalidRequest
	}
	if kind == ProviderOllama {
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return nil, ErrInvalidRequest
		}
		return NewOllamaProvider(WithOllamaBaseURL(endpoint), WithOllamaModel(model))
	}
	if parsed.Scheme != "https" || parsed.Hostname() == "" || key == "" {
		return nil, ErrInvalidRequest
	}
	options := []OpenAIOption{WithOpenAIBaseURL(endpoint), WithOpenAIDefaultModel(model), WithOpenAIAllowedHosts(parsed.Hostname())}
	if parsed.Port() != "" {
		port, portErr := strconv.Atoi(parsed.Port())
		if portErr != nil || port <= 0 || port > 65535 {
			return nil, ErrInvalidRequest
		}
		options = append(options, WithOpenAIAllowedPorts(port))
	}
	if kind == ProviderAzure {
		return NewAzureProvider(key, options...)
	}
	if kind != ProviderOpenAI {
		return nil, ErrInvalidRequest
	}
	return NewOpenAIProvider(key, options...)
}

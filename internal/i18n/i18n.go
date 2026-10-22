// Package i18n serves the message catalogue.
//
// The catalogue is the single source of truth for both the backend and the
// frontend: the frontend fetches it from this package over HTTP, so a
// translation can never drift between the two.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
)

//go:embed locales/*.json
var localeFS embed.FS

// DefaultLocale is used when the request carries no usable preference.
const DefaultLocale = "zh-CN"

// Catalogue holds the parsed messages of one locale.
type Catalogue struct {
	Locale   string
	messages map[string]string
}

// Catalogue returns the parsed messages of one locale.
func (c *Catalogue) Message(key string) (string, bool) {
	value, ok := c.messages[key]
	return value, ok
}

// Lookup resolves a message key with parameters. The key itself is returned
// when it is unknown, so a missing translation degrades into a readable
// identifier rather than an empty string.
func (c *Catalogue) Lookup(key string, params map[string]any) string {
	message, ok := c.messages[key]
	if !ok {
		return key
	}
	return interpolate(message, params)
}

// interpolate replaces {name} placeholders with the given parameters.
func interpolate(message string, params map[string]any) string {
	if len(params) == 0 {
		return message
	}
	for name, value := range params {
		placeholder := "{" + name + "}"
		replacement := fmt.Sprintf("%v", value)
		message = strings.ReplaceAll(message, placeholder, replacement)
	}
	return message
}

// Store loads and caches catalogues.
type Store struct {
	mu         sync.RWMutex
	catalogues map[string]*Catalogue
}

// NewStore loads every embedded locale.
func NewStore() (*Store, error) {
	store := &Store{catalogues: map[string]*Catalogue{}}

	entries, err := fs.ReadDir(localeFS, "locales")
	if err != nil {
		return nil, fmt.Errorf("read embedded locales: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		locale := strings.TrimSuffix(entry.Name(), ".json")
		raw, err := localeFS.ReadFile("locales/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read locale %s: %w", locale, err)
		}
		messages := map[string]string{}
		if err := json.Unmarshal(raw, &messages); err != nil {
			return nil, fmt.Errorf("parse locale %s: %w", locale, err)
		}
		store.catalogues[locale] = &Catalogue{Locale: locale, messages: messages}
	}
	return store, nil
}

// Get returns the catalogue for a locale, falling back to the default locale
// and finally to an empty catalogue.
func (s *Store) Get(locale string) *Catalogue {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if catalogue, ok := s.catalogues[locale]; ok {
		return catalogue
	}
	if catalogue, ok := s.catalogues[DefaultLocale]; ok {
		return catalogue
	}
	return &Catalogue{Locale: locale, messages: map[string]string{}}
}

// Locales returns the available locale identifiers, sorted.
func (s *Store) Locales() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	locales := make([]string, 0, len(s.catalogues))
	for locale := range s.catalogues {
		locales = append(locales, locale)
	}
	sort.Strings(locales)
	return locales
}

// Raw returns the message map of a locale, for delivery to the frontend.
func (s *Store) Raw(locale string) map[string]string {
	catalogue := s.Get(locale)
	out := make(map[string]string, len(catalogue.messages))
	for key, value := range catalogue.messages {
		out[key] = value
	}
	return out
}

// Keys returns every message key of the default locale. The CI check uses it
// to prove that a newly referenced key exists in every locale.
func (s *Store) Keys() []string {
	catalogue := s.Get(DefaultLocale)
	keys := make([]string, 0, len(catalogue.messages))
	for key := range catalogue.messages {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

package i18n

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreLoadsEveryEmbeddedLocale(t *testing.T) {
	store, err := NewStore()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}

	locales := store.Locales()
	if len(locales) < 2 {
		t.Fatalf("expected at least two locales, got %v", locales)
	}

	found := map[string]bool{}
	for _, locale := range locales {
		found[locale] = true
	}
	for _, want := range []string{"zh-CN", "en"} {
		if !found[want] {
			t.Fatalf("locale %q is missing, got %v", want, locales)
		}
	}
}

func TestEveryLocaleHasTheSameKeys(t *testing.T) {
	store, err := NewStore()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}

	reference := store.Keys()
	if len(reference) == 0 {
		t.Fatal("the default locale has no messages")
	}
	referenceSet := map[string]bool{}
	for _, key := range reference {
		referenceSet[key] = true
	}

	for _, locale := range store.Locales() {
		messages := store.Raw(locale)
		for _, key := range reference {
			if _, ok := messages[key]; !ok {
				t.Errorf("locale %q is missing key %q", locale, key)
			}
		}
		for key := range messages {
			if !referenceSet[key] {
				t.Errorf("locale %q has an extra key %q", locale, key)
			}
		}
	}
}

// TestLocalesAreValidJSON guards against a malformed catalogue slipping in.
func TestLocalesAreValidJSON(t *testing.T) {
	entries, err := os.ReadDir("locales")
	if err != nil {
		t.Fatalf("read locales: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join("locales", entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		messages := map[string]string{}
		if err := json.Unmarshal(raw, &messages); err != nil {
			t.Errorf("%s is not valid JSON: %v", path, err)
		}
		if len(messages) == 0 {
			t.Errorf("%s is empty", path)
		}
	}
}

func TestLookupFallsBackToTheKey(t *testing.T) {
	store, err := NewStore()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	catalogue := store.Get("zh-CN")

	if got := catalogue.Lookup("does.not.exist", nil); got != "does.not.exist" {
		t.Fatalf("expected the key to be returned, got %q", got)
	}
}

func TestLookupInterpolatesParameters(t *testing.T) {
	store, err := NewStore()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	catalogue := store.Get("zh-CN")

	got := catalogue.Lookup("mailbox.quota_exceeded", map[string]any{
		"address": "a@example.com",
		"limit":   1024,
	})
	if !strings.Contains(got, "a@example.com") || !strings.Contains(got, "1024") {
		t.Fatalf("parameters were not interpolated: %q", got)
	}
}

func TestUnknownLocaleFallsBackToDefault(t *testing.T) {
	store, err := NewStore()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	if catalogue := store.Get("xx-YY"); catalogue.Locale != DefaultLocale {
		t.Fatalf("expected fallback to %q, got %q", DefaultLocale, catalogue.Locale)
	}
}

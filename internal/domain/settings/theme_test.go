package settings_test

import (
	"encoding/json"
	"testing"

	"github.com/bushixiucai/urmail/internal/domain/settings"
)

func TestBuildEffectiveTheme(t *testing.T) {
	brandTheme := json.RawMessage(`{"accent": "#00f", "dark_mode": false}`)
	logo := "https://cdn.example.com/logo.png"
	color := "#0a7cff"

	src := settings.BrandThemeSource{
		BrandID:      3,
		BrandSlug:    "acme",
		Theme:        brandTheme,
		LogoURL:      &logo,
		PrimaryColor: &color,
	}

	entries := []settings.SettingEntry{
		// platform base
		{Scope: settings.ScopePlatform, ScopeID: 0, Key: "theme", Value: json.RawMessage(`{"dark_mode": true}`)},
		// brand override (deep merges into platform)
		{Scope: settings.ScopeBrand, ScopeID: 3, Key: "theme", Value: json.RawMessage(`{"accent": "#f00"}`)},
		// tenant override wins
		{Scope: settings.ScopeTenant, ScopeID: 42, Key: "theme", Value: json.RawMessage(`{"dark_mode": {"enabled": true}}`)},
		// unrelated key must be ignored
		{Scope: settings.ScopeTenant, ScopeID: 42, Key: "smtp", Value: json.RawMessage(`{"evil": true}`)},
	}

	eff, err := settings.BuildEffectiveTheme(src, entries)
	if err != nil {
		t.Fatalf("BuildEffectiveTheme: %v", err)
	}

	if eff.BrandID != 3 || eff.BrandSlug != "acme" {
		t.Errorf("unexpected brand identity: %+v", eff)
	}
	if eff.LogoURL == nil || *eff.LogoURL != logo {
		t.Errorf("logo_url not propagated: %+v", eff.LogoURL)
	}
	if eff.PrimaryColor == nil || *eff.PrimaryColor != color {
		t.Errorf("primary_color not propagated: %+v", eff.PrimaryColor)
	}

	var m map[string]any
	if err := json.Unmarshal(eff.Theme, &m); err != nil {
		t.Fatalf("merged theme not valid JSON: %v", err)
	}
	if m["accent"] != "#f00" {
		t.Errorf("brand accent override lost: %v", m["accent"])
	}
	// tenant deep-merged into the boolean from platform
	dm, ok := m["dark_mode"].(map[string]any)
	if !ok || dm["enabled"] != true {
		t.Errorf("tenant theme override lost: %v", m["dark_mode"])
	}
	if _, ok := m["evil"]; ok {
		t.Error("non-theme settings key must not leak into theme")
	}
}

func TestBuildEffectiveThemeNoOverrides(t *testing.T) {
	src := settings.BrandThemeSource{BrandID: 1, BrandSlug: "b", Theme: json.RawMessage(`{"a": 1}`)}
	eff, err := settings.BuildEffectiveTheme(src, nil)
	if err != nil {
		t.Fatalf("BuildEffectiveTheme: %v", err)
	}
	if string(eff.Theme) != `{"a":1}` {
		t.Errorf("brand theme should pass through, got %s", eff.Theme)
	}
}

func TestEffectivePrimaryColorFollowsResolvedTheme(t *testing.T) {
	color := "#111111"
	src := settings.BrandThemeSource{Theme: json.RawMessage(`{"primary":"#222222"}`), PrimaryColor: &color}
	entries := []settings.SettingEntry{{Scope: settings.ScopePlatform, Key: "theme", Value: json.RawMessage(`{"primary":"#333333"}`)}, {Scope: settings.ScopeBrand, Key: "theme", Value: json.RawMessage(`{"primary":"#444444"}`)}, {Scope: settings.ScopeTenant, Key: "theme", Value: json.RawMessage(`{"primary":"#555555"}`)}}
	for i, want := range []string{"#222222", "#222222", "#444444", "#555555"} {
		eff, err := settings.BuildEffectiveTheme(src, entries[:i])
		if err != nil {
			t.Fatal(err)
		}
		if eff.PrimaryColor == nil || *eff.PrimaryColor != want {
			t.Fatalf("scope depth %d: %+v", i, eff)
		}
	}
}

func TestBrandRowAbovePlatformTheme(t *testing.T) {
	platform := []settings.SettingEntry{{Scope: settings.ScopePlatform, Key: "theme", Value: json.RawMessage(`{"primary":"#123456"}`)}}
	rowColor := "#654321"
	for _, tc := range []struct {
		source settings.BrandThemeSource
		want   string
	}{
		{settings.BrandThemeSource{Theme: json.RawMessage(`{}`)}, "#123456"},
		{settings.BrandThemeSource{PrimaryColor: &rowColor, Theme: json.RawMessage(`{}`)}, "#654321"},
		{settings.BrandThemeSource{PrimaryColor: &rowColor, Theme: json.RawMessage(`{"primary":"#abcdef"}`)}, "#abcdef"},
	} {
		eff, err := settings.BuildEffectiveTheme(tc.source, platform)
		if err != nil {
			t.Fatal(err)
		}
		if eff.PrimaryColor == nil || *eff.PrimaryColor != tc.want {
			t.Fatalf("resolved %+v want %s", eff, tc.want)
		}
	}
}

package settings

import (
	"encoding/json"
	"fmt"
)

// BrandThemeSource is the brand row data relevant to theming (brands.theme,
// logo_url, favicon_url, primary_color). The store layer fills it via the
// PlatformGetBrandThemeByTenant / PlatformListBrandThemes queries.
type BrandThemeSource struct {
	BrandID      int64           `json:"brand_id"`
	BrandSlug    string          `json:"brand_slug"`
	Theme        json.RawMessage `json:"theme"`
	LogoURL      *string         `json:"logo_url,omitempty"`
	FaviconURL   *string         `json:"favicon_url,omitempty"`
	PrimaryColor *string         `json:"primary_color,omitempty"`
}

// EffectiveTheme is the resolved theme handed to the API / reconcile snapshot:
// brand row fields with the settings "theme" key merged on top, following the
// platform < brand < tenant precedence.
type EffectiveTheme struct {
	BrandID      int64   `json:"brand_id"`
	BrandSlug    string  `json:"brand_slug"`
	LogoURL      *string `json:"logo_url,omitempty"`
	FaviconURL   *string `json:"favicon_url,omitempty"`
	PrimaryColor *string `json:"primary_color,omitempty"`
	// Theme is the merged brands.theme JSON (deep-merged with the
	// platform/brand/tenant settings rows keyed "theme").
	Theme json.RawMessage `json:"theme"`
}

// ThemeKey is the settings key under which theme overrides are stored.
const ThemeKey = "theme"

// BuildEffectiveTheme merges the brand's theme column with any "theme"
// settings entries (platform -> brand -> tenant precedence, deep merge) and
// returns the effective theme for the tenant.
func BuildEffectiveTheme(src BrandThemeSource, entries []SettingEntry) (*EffectiveTheme, error) {
	merged := []byte(src.Theme)
	if len(merged) == 0 {
		merged = json.RawMessage(`{}`)
	}

	for _, scope := range []Scope{ScopePlatform, ScopeBrand, ScopeTenant} {
		for _, e := range entries {
			if e.Key != ThemeKey || e.Scope != scope {
				continue
			}
			var err error
			merged, err = DeepMergeJSON(merged, e.Value)
			if err != nil {
				return nil, fmt.Errorf("merge %s theme setting: %w", scope, err)
			}
		}
	}

	primary := src.PrimaryColor
	var resolved map[string]json.RawMessage
	if err := json.Unmarshal(merged, &resolved); err == nil {
		var value string
		if raw, ok := resolved["primary"]; ok && json.Unmarshal(raw, &value) == nil && value != "" {
			primary = &value
		}
	}
	return &EffectiveTheme{
		BrandID:      src.BrandID,
		BrandSlug:    src.BrandSlug,
		LogoURL:      src.LogoURL,
		FaviconURL:   src.FaviconURL,
		PrimaryColor: primary,
		Theme:        merged,
	}, nil
}

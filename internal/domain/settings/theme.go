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

// BuildEffectiveTheme deep-merges platform settings, the brand row, brand
// settings, and tenant settings in that order. Row theme.primary takes
// precedence over the legacy row primary_color field.
func BuildEffectiveTheme(src BrandThemeSource, entries []SettingEntry) (*EffectiveTheme, error) {
	merged := []byte(`{}`)

	for _, scope := range []Scope{ScopePlatform, ScopeBrand, ScopeTenant} {
		// The row is the brand layer, above platform defaults. Explicit row
		// theme.primary wins over the legacy primary_color column.
		if scope == ScopeBrand {
			if src.PrimaryColor != nil {
				base, _ := json.Marshal(map[string]string{"primary": *src.PrimaryColor})
				var err error
				merged, err = DeepMergeJSON(merged, base)
				if err != nil {
					return nil, fmt.Errorf("merge brand primary color: %w", err)
				}
			}
			if len(src.Theme) > 0 {
				var err error
				merged, err = DeepMergeJSON(merged, src.Theme)
				if err != nil {
					return nil, fmt.Errorf("merge brand row theme: %w", err)
				}
			}
		}
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

package settings

import (
	"encoding/json"
	"fmt"
)

// Scope represents the level of a setting in the 3-layer model.
type Scope string

const (
	ScopePlatform Scope = "platform"
	ScopeBrand    Scope = "brand"
	ScopeTenant   Scope = "tenant"
)

// SettingEntry represents a single setting row from the database.
type SettingEntry struct {
	Scope   Scope           `json:"scope"`
	ScopeID int64           `json:"scope_id"`
	Key     string          `json:"key"`
	Value   json.RawMessage `json:"value"`
}

// DeepMergeJSON merges overlay JSON onto base JSON.
// If both base and overlay are JSON objects, keys are recursively merged.
// Otherwise, overlay completely replaces base.
func DeepMergeJSON(base, overlay []byte) ([]byte, error) {
	if len(base) == 0 {
		return overlay, nil
	}
	if len(overlay) == 0 {
		return base, nil
	}

	var baseVal, overlayVal any
	if err := json.Unmarshal(base, &baseVal); err != nil {
		return nil, fmt.Errorf("unmarshal base JSON: %w", err)
	}
	if err := json.Unmarshal(overlay, &overlayVal); err != nil {
		return nil, fmt.Errorf("unmarshal overlay JSON: %w", err)
	}

	merged := mergeValues(baseVal, overlayVal)
	return json.Marshal(merged)
}

func mergeValues(base, overlay any) any {
	baseMap, isBaseMap := base.(map[string]any)
	overlayMap, isOverlayMap := overlay.(map[string]any)

	if isBaseMap && isOverlayMap {
		out := make(map[string]any, len(baseMap)+len(overlayMap))
		for k, v := range baseMap {
			out[k] = v
		}
		for k, v := range overlayMap {
			if existing, exists := out[k]; exists {
				out[k] = mergeValues(existing, v)
			} else {
				out[k] = v
			}
		}
		return out
	}

	// For scalars, arrays, or type mismatches: overlay wins
	return overlay
}

// Merge3Layers takes settings entries from platform, brand, and tenant scopes,
// and merges them per key following precedence: Platform < Brand < Tenant.
func Merge3Layers(entries []SettingEntry) (map[string]json.RawMessage, error) {
	// Separate entries by scope
	byScope := map[Scope]map[string][]byte{
		ScopePlatform: make(map[string][]byte),
		ScopeBrand:    make(map[string][]byte),
		ScopeTenant:   make(map[string][]byte),
	}

	allKeys := make(map[string]bool)
	for _, e := range entries {
		allKeys[e.Key] = true
		byScope[e.Scope][e.Key] = e.Value
	}

	merged := make(map[string]json.RawMessage, len(allKeys))
	for key := range allKeys {
		var current []byte

		// 1. Platform base
		if val, ok := byScope[ScopePlatform][key]; ok {
			current = val
		}

		// 2. Brand override
		if val, ok := byScope[ScopeBrand][key]; ok {
			var err error
			current, err = DeepMergeJSON(current, val)
			if err != nil {
				return nil, fmt.Errorf("merge brand setting %q: %w", key, err)
			}
		}

		// 3. Tenant override
		if val, ok := byScope[ScopeTenant][key]; ok {
			var err error
			current, err = DeepMergeJSON(current, val)
			if err != nil {
				return nil, fmt.Errorf("merge tenant setting %q: %w", key, err)
			}
		}

		merged[key] = current
	}

	return merged, nil
}

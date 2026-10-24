package billing

import (
	"encoding/json"
	"reflect"
)

// UpgradeCompatible requires an explicit source relationship and preserves
// enabled capabilities. Only documented quota values may increase numerically;
// unknown feature semantics must remain equal rather than being guessed.
func UpgradeCompatible(source string, oldJSON, newJSON []byte) bool {
	var old, next map[string]any
	if json.Unmarshal(oldJSON, &old) != nil || json.Unmarshal(newJSON, &next) != nil {
		return false
	}
	sources, ok := next["upgrade_from"].([]any)
	if !ok {
		return false
	}
	allowed := false
	for _, v := range sources {
		if v == source {
			allowed = true
		}
	}
	if !allowed {
		return false
	}
	for key, value := range old {
		switch key {
		case "upgrade_from", "downgrade_from", "next_period_from", "trial_eligible", "self_service_purchase", "default_mailbox_quota_bytes":
			continue
		}
		if !preservesCapability(key, value, next[key]) {
			return false
		}
	}
	return true
}
func preservesCapability(key string, old, next any) bool {
	switch value := old.(type) {
	case nil:
		return true
	case bool:
		return !value || next == true
	case float64:
		target, ok := next.(float64)
		if !ok {
			return value == 0 && next == nil
		}
		switch key {
		case "calls", "tokens", "cost_micros", "concurrency", "mailbox_calls", "marketing_daily_recipient_limit", "marketing_rate_per_minute":
			return value >= 0 && target >= value
		}
		return value == target
	case map[string]any:
		target, ok := next.(map[string]any)
		if !ok {
			return len(value) == 0
		}
		for child, v := range value {
			if !preservesCapability(child, v, target[child]) {
				return false
			}
		}
		return true
	case []any:
		return reflect.DeepEqual(value, next)
	default:
		return reflect.DeepEqual(old, next)
	}
}

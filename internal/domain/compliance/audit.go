// Package compliance enforces audit redaction and legal preservation rules.
package compliance

import (
	"encoding/json"
	"strings"
)

// SensitiveFields lists keys that must never be stored in cleartext in audit details.
var SensitiveFields = map[string]bool{
	"password":      true,
	"password_hash": true,
	"secret":        true,
	"mfa_secret":    true,
	"private_key":   true,
	"token":         true,
	"api_key":       true,
	"card_number":   true,
}

// MaskAuditDetail recursively scrubs sensitive fields from a JSON detail map before persisting.
func MaskAuditDetail(detail map[string]any) ([]byte, error) {
	if detail == nil {
		return []byte("{}"), nil
	}

	sanitized := maskMap(detail)
	return json.Marshal(sanitized)
}

func maskMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		lowerKey := strings.ToLower(k)
		if SensitiveFields[lowerKey] {
			out[k] = "[REDACTED]"
			continue
		}

		if subMap, ok := v.(map[string]any); ok {
			out[k] = maskMap(subMap)
		} else {
			out[k] = v
		}
	}
	return out
}

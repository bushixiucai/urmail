// Package mailboxsettings validates mailbox-user self-service preferences.
// It never touches the database or HTTP transport.
package mailboxsettings

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Bounded errors surfaced to the mailbox session as typed 400 responses.
var (
	// ErrDisplayName rejects an unusable display name.
	ErrDisplayName = errors.New("MAILBOX_SETTINGS_DISPLAY_NAME_INVALID")
	// ErrSignature rejects an unusable signature body.
	ErrSignature = errors.New("MAILBOX_SETTINGS_SIGNATURE_INVALID")
	// ErrPreferences rejects preference JSON outside the bounded shape.
	ErrPreferences = errors.New("MAILBOX_SETTINGS_PREFERENCES_INVALID")
	// ErrVersion rejects a non-positive or overflowing optimistic version.
	ErrVersion = errors.New("MAILBOX_SETTINGS_VERSION_INVALID")
)

const (
	// MaxDisplayName bounds the From display name in runes.
	MaxDisplayName = 128
	// MaxSignatureText bounds the plain-text signature in bytes.
	MaxSignatureText = 8192
	// MaxSignatureHTML bounds the HTML signature in bytes.
	MaxSignatureHTML = 16384
	// MaxPreferencesBytes bounds the serialized preferences object.
	MaxPreferencesBytes = 8192
	// MaxPreferenceKeys bounds the number of preference entries.
	MaxPreferenceKeys = 64
)

func noControl(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t'
	})
}

// DisplayName normalizes and validates a From display name. An empty name
// means "show the address alone".
func DisplayName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxDisplayName || !noControl(name) || strings.ContainsAny(name, "\r\n") {
		return "", ErrDisplayName
	}
	return name, nil
}

// Signature normalizes and validates one signature part. Signatures may
// contain intentional newlines; other control characters are rejected.
func Signature(raw string, limit int) (string, error) {
	if !utf8.ValidString(raw) || len(raw) > limit || !noControl(raw) {
		return "", ErrSignature
	}
	return raw, nil
}

// Preferences validates a bounded, flat-ish JSON object of user preferences.
// Values stay caller-defined (booleans, strings, numbers) but the whole
// document stays small and object-shaped so it can be stored as jsonb.
func Preferences(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage("{}"), nil
	}
	if len(raw) > MaxPreferencesBytes || !utf8.Valid(raw) {
		return nil, ErrPreferences
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return nil, ErrPreferences
	}
	if len(object) > MaxPreferenceKeys {
		return nil, ErrPreferences
	}
	for key, value := range object {
		if key == "" || len(key) > 64 || strings.ContainsFunc(key, unicode.IsControl) {
			return nil, ErrPreferences
		}
		var scalar any
		if json.Unmarshal(value, &scalar) != nil {
			return nil, ErrPreferences
		}
		switch scalar.(type) {
		case string, float64, bool, nil:
		default:
			return nil, ErrPreferences
		}
	}
	normalized, err := json.Marshal(object)
	if err != nil || len(normalized) > MaxPreferencesBytes {
		return nil, ErrPreferences
	}
	return normalized, nil
}

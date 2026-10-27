package mailboxsettings

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDisplayName(t *testing.T) {
	name, err := DisplayName("  Alice 张三 \n")
	if err != nil || name != "Alice 张三" {
		t.Fatalf("display name %q %v", name, err)
	}
	if _, err := DisplayName(strings.Repeat("x", MaxDisplayName+1)); !errors.Is(err, ErrDisplayName) {
		t.Fatalf("too long: %v", err)
	}
	if _, err := DisplayName("bad\x00name"); !errors.Is(err, ErrDisplayName) {
		t.Fatalf("control char: %v", err)
	}
}

func TestSignature(t *testing.T) {
	sig, err := Signature("line1\nline2\t", MaxSignatureText)
	if err != nil || sig != "line1\nline2\t" {
		t.Fatalf("signature %q %v", sig, err)
	}
	if _, err := Signature(strings.Repeat("x", MaxSignatureHTML+1), MaxSignatureHTML); !errors.Is(err, ErrSignature) {
		t.Fatalf("too long: %v", err)
	}
	if _, err := Signature("bad\x00", MaxSignatureText); !errors.Is(err, ErrSignature) {
		t.Fatalf("control char: %v", err)
	}
}

func TestPreferences(t *testing.T) {
	out, err := Preferences(json.RawMessage(`{"thread":true,"density":"cozy"}`))
	if err != nil {
		t.Fatalf("valid prefs: %v", err)
	}
	var got map[string]any
	if json.Unmarshal(out, &got) != nil || len(got) != 2 {
		t.Fatalf("normalized prefs %s", out)
	}
	if out, err := Preferences(nil); err != nil || string(out) != "{}" {
		t.Fatalf("empty prefs %s %v", out, err)
	}
	for _, bad := range []string{
		`[1,2]`,
		`{"nested":{"deep":1}}`,
		`{"key":"` + strings.Repeat("v", MaxPreferencesBytes) + `"}`,
		`not json`,
	} {
		if _, err := Preferences(json.RawMessage(bad)); !errors.Is(err, ErrPreferences) {
			t.Fatalf("prefs %q: got %v", bad, err)
		}
	}
	// Objects stay rejected even when bounded.
	if _, err := Preferences(json.RawMessage(`{"nested":{"a":1}}`)); !errors.Is(err, ErrPreferences) {
		t.Fatalf("nested object accepted: %v", err)
	}
}

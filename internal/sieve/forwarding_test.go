package sieve

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func quote(value string) string { return "\"" + value + "\"" }

func TestCompileForwardingRoundTrip(t *testing.T) {
	base := Rules{
		Rules:    []Rule{{Header: "from", Match: "contains", Value: "boss", Action: "keep"}},
		Vacation: Vacation{Enabled: false},
	}
	if _, err := Compile(base); err != nil {
		t.Fatalf("base compile: %v", err)
	}
	redirectCopy := "redirect :copy " + quote("archive@example.test") + ";"
	redirectOnly := "redirect " + quote("archive@example.test") + "; stop;"
	cases := []struct {
		name    string
		fwd     Forwarding
		want    string
		notWant string
	}{
		{"keep-copy", Forwarding{Enabled: true, Target: "archive@example.test", KeepCopy: true}, redirectCopy, redirectOnly},
		{"forward-only", Forwarding{Enabled: true, Target: "archive@example.test"}, redirectOnly, ":copy"},
	}
	for _, tc := range cases {
		rules := base
		fwd := tc.fwd
		rules.Forwarding = &fwd
		script, err := Compile(rules)
		if err != nil {
			t.Fatalf("%s compile: %v", tc.name, err)
		}
		if !strings.Contains(script, tc.want) {
			t.Fatalf("%s script %q", tc.name, script)
		}
		if tc.notWant != "" && strings.Contains(script, tc.notWant) {
			t.Fatalf("%s script must not contain %q: %s", tc.name, tc.notWant, script)
		}
		decoded, err := Decode(script)
		if err != nil {
			t.Fatalf("%s decode: %v", tc.name, err)
		}
		if decoded.Forwarding == nil || decoded.Forwarding.Target != fwd.Target || decoded.Forwarding.KeepCopy != fwd.KeepCopy {
			t.Fatalf("%s round trip %+v", tc.name, decoded.Forwarding)
		}
		again, err := Compile(decoded)
		if err != nil || again != script {
			t.Fatalf("%s canonical mismatch: %v", tc.name, err)
		}
	}
}

func TestCompileForwardingRejectsUnsafeTargets(t *testing.T) {
	for _, target := range []string{"", "not-an-address", "a@b", "UPPER@example.test", "user@@example.test", "user@..", strings.Repeat("x", 250) + "@example.test"} {
		rules := Rules{Forwarding: &Forwarding{Enabled: true, Target: target}}
		if _, err := Compile(rules); !errors.Is(err, ErrInvalidRules) {
			t.Fatalf("target %q: got %v", target, err)
		}
	}
}

// Scripts stored before forwarding existed must still decode and recompile
// byte-for-byte.
func TestDecodeLegacyScriptWithoutForwarding(t *testing.T) {
	legacy := Rules{
		Vacation: Vacation{Enabled: true, Subject: "away", Message: "I am away", Days: 3, From: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)},
	}
	script, err := Compile(legacy)
	if err != nil {
		t.Fatalf("compile legacy: %v", err)
	}
	if strings.Contains(script, "redirect") {
		t.Fatalf("legacy script unexpectedly redirects: %s", script)
	}
	decoded, err := Decode(script)
	if err != nil {
		t.Fatalf("decode legacy: %v", err)
	}
	if decoded.Forwarding != nil {
		t.Fatalf("legacy forwarding should be nil: %+v", decoded.Forwarding)
	}
	again, err := Compile(decoded)
	if err != nil || again != script {
		t.Fatalf("legacy canonical drift: %v", err)
	}
}

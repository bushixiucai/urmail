package sieve

import (
	"strings"
	"testing"
	"time"
)

func TestStructuredRulesRejectCodeAndPathInjection(t *testing.T) {
	rules := Rules{Rules: []Rule{{Header: "subject", Match: "contains", Value: `\"; redirect "steal@example.test"; #`, Action: "fileinto", Folder: "Reviewed"}}}
	script, err := Compile(rules)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Decode(script); err != nil {
		t.Fatal(err)
	}
	if _, err = Decode(script + "redirect \"steal@example.test\";\n"); err == nil {
		t.Fatal("arbitrary Sieve appended to generated source accepted")
	}
	for _, folder := range []string{"../outside", "Inbox/../outside", "/absolute", "Inbox\nfileinto"} {
		rules.Rules[0].Folder = folder
		if _, err = Compile(rules); err == nil {
			t.Fatalf("unsafe folder accepted %q", folder)
		}
	}
	rules.Rules[0].Action = "redirect"
	if _, err = Compile(rules); err == nil {
		t.Fatal("external forwarding action accepted")
	}
}

func TestVacationFiniteDatesAndLoopSuppression(t *testing.T) {
	now := time.Now().UTC()
	rules := Rules{Vacation: Vacation{Enabled: true, Subject: "Away", Message: "Please contact support.", Days: 7, From: now, Until: now.Add(7 * 24 * time.Hour)}}
	script, err := Compile(rules)
	if err != nil {
		t.Fatal(err)
	}
	for _, term := range []string{"Auto-Submitted", "List-Id", "Precedence", `not envelope :is "from" ""`, `vacation :days 7`, `:handle`, `:zone "+0000"`} {
		if !strings.Contains(script, term) {
			t.Fatalf("missing suppression %s", term)
		}
	}
	rules.Vacation.Until = time.Time{}
	if _, err = Compile(rules); err == nil {
		t.Fatal("indefinite vacation accepted")
	}
	rules.Vacation.Until = now.Add(24 * time.Hour)
	rules.Vacation.Days = 0
	if _, err = Compile(rules); err == nil {
		t.Fatal("duplicate reply suppression disabled")
	}
}

func TestForwardCopyRequiresExtensionAndAcceptsOnlyExactLegacyForm(t *testing.T) {
	rules := Rules{Forwarding: &Forwarding{Enabled: true, Target: "copy@example.test", KeepCopy: true}}
	script, err := Compile(rules)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "require \"copy\";\n") {
		t.Fatal("redirect :copy lacks its required capability")
	}
	legacy := strings.Replace(script, "require \"copy\";\n", "", 1)
	if _, err = Decode(legacy); err != nil {
		t.Fatalf("historical policy cannot be recovered: %v", err)
	}
	if _, err = Decode(legacy + "redirect \"attacker@example.test\";\n"); err == nil {
		t.Fatal("legacy compatibility accepted an injected action")
	}
	decoded, err := Decode(legacy)
	if err != nil {
		t.Fatal(err)
	}
	fixed, err := Compile(decoded)
	if err != nil || fixed != script {
		t.Fatal("legacy policy did not normalize to the exact intended program")
	}
}

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
	for _, term := range []string{"Auto-Submitted", "List-Id", "Precedence", `not envelope :is "from" ""`, `vacation :days 7`, `:handle`, `:zone "+0000"`, `not hasflag "urmail_spam"`} {
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

func TestCompoundConditionsAllAndAny(t *testing.T) {
	// MatchType: any
	rulesAny := Rules{Rules: []Rule{{
		Conditions: []RuleCondition{
			{Header: "subject", Match: "contains", Value: "invoice"},
			{Header: "from", Match: "contains", Value: "billing@"},
		},
		MatchType: "any",
		Action:    "fileinto",
		Folder:    "Finance",
	}}}
	scriptAny, err := Compile(rulesAny)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(scriptAny, `if anyof (header :contains "subject" "invoice", header :contains "from" "billing@") {`) {
		t.Fatalf("expected anyof compound condition, got:\n%s", scriptAny)
	}
	if _, err = Decode(scriptAny); err != nil {
		t.Fatalf("failed to decode scriptAny: %v", err)
	}

	// MatchType: all
	rulesAll := Rules{Rules: []Rule{{
		Conditions: []RuleCondition{
			{Header: "subject", Match: "contains", Value: "urgent"},
			{Header: "from", Match: "is", Value: "boss@example.com"},
		},
		MatchType: "all",
		Action:    "fileinto",
		Folder:    "Priority",
	}}}
	scriptAll, err := Compile(rulesAll)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(scriptAll, `if allof (header :contains "subject" "urgent", header :is "from" "boss@example.com") {`) {
		t.Fatalf("expected allof compound condition, got:\n%s", scriptAll)
	}
	if _, err = Decode(scriptAll); err != nil {
		t.Fatalf("failed to decode scriptAll: %v", err)
	}
}

func TestMultiScriptOrderingDiscardForwardingVacationFolder(t *testing.T) {
	now := time.Now().UTC()
	rules := Rules{
		Rules: []Rule{
			{Header: "from", Match: "is", Value: "spammer@bad.test", Action: "discard"},
			{Header: "subject", Match: "contains", Value: "receipt", Action: "fileinto", Folder: "Receipts"},
		},
		Forwarding: &Forwarding{Enabled: true, Target: "archive@backup.test", KeepCopy: true},
		Vacation:   Vacation{Enabled: true, Subject: "Out", Message: "Away", Days: 3, From: now, Until: now.Add(48 * time.Hour)},
	}
	script, err := Compile(rules)
	if err != nil {
		t.Fatal(err)
	}

	idxDiscard := strings.Index(script, "discard; stop;")
	idxForward := strings.Index(script, "redirect :copy")
	idxVacation := strings.Index(script, "vacation :days")
	idxFileinto := strings.Index(script, `fileinto :create "Receipts"`)

	if idxDiscard < 0 || idxForward < 0 || idxVacation < 0 || idxFileinto < 0 {
		t.Fatalf("missing required sieve statements:\n%s", script)
	}
	if idxDiscard >= idxForward || idxForward >= idxVacation || idxVacation >= idxFileinto {
		t.Fatalf("execution order violated: discard (%d) < forward (%d) < vacation (%d) < fileinto (%d)\n%s",
			idxDiscard, idxForward, idxVacation, idxFileinto, script)
	}
	if _, err = Decode(script); err != nil {
		t.Fatalf("failed to decode multi-script rule: %v", err)
	}
}

// Package sieve compiles a bounded structured rule set, never arbitrary code.
package sieve

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/mail/validation"
)

// ErrInvalidRules indicates an unsafe or malformed rule set.
var ErrInvalidRules = errors.New("invalid structured sieve rules")

const sourcePrefix = "# urmail-rules:"

// RuleCondition is one header match condition within a compound rule.
type RuleCondition struct {
	Header string `json:"header"`
	Match  string `json:"match"`
	Value  string `json:"value"`
}

// Rule is one bounded header condition (or compound conditions) and delivery action.
type Rule struct {
	Disabled   bool            `json:"disabled,omitempty"`
	Header     string          `json:"header,omitempty"`
	Match      string          `json:"match,omitempty"`
	Value      string          `json:"value,omitempty"`
	Conditions []RuleCondition `json:"conditions,omitempty"`
	MatchType  string          `json:"match_type,omitempty"` // "all" or "any"
	Action     string          `json:"action"`
	Folder     string          `json:"folder,omitempty"`
}

// Vacation is a finite, deduplicated automatic reply schedule.
type Vacation struct {
	Enabled bool      `json:"enabled"`
	Subject string    `json:"subject"`
	Message string    `json:"message"`
	Days    int       `json:"days"`
	From    time.Time `json:"from"`
	Until   time.Time `json:"until"`
}

// Forwarding is an explicit keep-and-forward or forward-only instruction.
// It is a pointer so scripts written before forwarding existed keep their
// canonical form byte-for-byte and still Decode.
type Forwarding struct {
	Enabled  bool   `json:"enabled"`
	Target   string `json:"target"`
	KeepCopy bool   `json:"keep_copy"`
}

// Rules contains the complete user-confirmed mailbox policy.
type Rules struct {
	Rules      []Rule      `json:"rules"`
	Forwarding *Forwarding `json:"forwarding,omitempty"`
	Vacation   Vacation    `json:"vacation"`
}

// forwardingTargetValid accepts one concrete mailbox address as target.
func forwardingTargetValid(target string) bool {
	if len(target) < 3 || len(target) > 254 || target != strings.ToLower(target) {
		return false
	}
	local, domain, ok := strings.Cut(target, "@")
	if !ok || strings.Contains(domain, "@") || len(local) == 0 || len(local) > 64 {
		return false
	}
	return validation.LocalPartRegex.MatchString(local) && validation.IsValidHostname(domain)
}

func literal(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", " ", "\n", " ").Replace(value) + `"`
}

func folderValid(value string) bool {
	if len(value) == 0 || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n\\") || strings.HasPrefix(value, "/") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." || part == "." || part == "" {
			return false
		}
	}
	return true
}

func effectiveConditions(rule Rule) ([]RuleCondition, string, error) {
	if len(rule.Conditions) > 0 {
		if len(rule.Conditions) > 10 {
			return nil, "", ErrInvalidRules
		}
		matchType := rule.MatchType
		if matchType == "" {
			matchType = "all"
		}
		if matchType != "all" && matchType != "any" {
			return nil, "", ErrInvalidRules
		}
		for _, c := range rule.Conditions {
			if c.Header != "subject" && c.Header != "from" && c.Header != "to" && c.Header != "cc" {
				return nil, "", ErrInvalidRules
			}
			if c.Match != "contains" && c.Match != "is" {
				return nil, "", ErrInvalidRules
			}
			if len(c.Value) == 0 || len(c.Value) > 2000 || strings.ContainsRune(c.Value, 0) {
				return nil, "", ErrInvalidRules
			}
		}
		return rule.Conditions, matchType, nil
	}

	if rule.Header != "subject" && rule.Header != "from" && rule.Header != "to" && rule.Header != "cc" {
		return nil, "", ErrInvalidRules
	}
	if rule.Match != "contains" && rule.Match != "is" {
		return nil, "", ErrInvalidRules
	}
	if len(rule.Value) == 0 || len(rule.Value) > 2000 || strings.ContainsRune(rule.Value, 0) {
		return nil, "", ErrInvalidRules
	}
	return []RuleCondition{{
		Header: rule.Header,
		Match:  rule.Match,
		Value:  rule.Value,
	}}, "all", nil
}

func renderCondition(conds []RuleCondition, matchType string) string {
	if len(conds) == 1 {
		return fmt.Sprintf("if header :%s %s %s {", conds[0].Match, literal(conds[0].Header), literal(conds[0].Value))
	}
	parts := make([]string, len(conds))
	for i, c := range conds {
		parts[i] = fmt.Sprintf("header :%s %s %s", c.Match, literal(c.Header), literal(c.Value))
	}
	if matchType == "any" {
		return fmt.Sprintf("if anyof (%s) {", strings.Join(parts, ", "))
	}
	return fmt.Sprintf("if allof (%s) {", strings.Join(parts, ", "))
}

// Compile emits a canonical Sieve program without arbitrary extensions.
func Compile(rules Rules) (string, error) {
	if len(rules.Rules) > 50 {
		return "", ErrInvalidRules
	}
	raw, err := json.Marshal(rules)
	if err != nil || len(raw) > 32768 {
		return "", ErrInvalidRules
	}
	var out strings.Builder
	out.WriteString(sourcePrefix + base64.RawStdEncoding.EncodeToString(raw) + "\n")
	out.WriteString("require [\"fileinto\",\"mailbox\",\"vacation\",\"envelope\",\"date\",\"relational\",\"imap4flags\"];\n")
	if rules.Forwarding != nil && rules.Forwarding.Enabled && rules.Forwarding.KeepCopy {
		out.WriteString("require \"copy\";\n")
	}

	type compiledRule struct {
		rule      Rule
		conds     []RuleCondition
		matchType string
	}
	compiled := make([]compiledRule, len(rules.Rules))
	for i, rule := range rules.Rules {
		conds, matchType, err := effectiveConditions(rule)
		if err != nil {
			return "", err
		}
		if rule.Action != "fileinto" && rule.Action != "keep" && rule.Action != "discard" {
			return "", ErrInvalidRules
		}
		if rule.Action == "fileinto" && !folderValid(rule.Folder) {
			return "", ErrInvalidRules
		}
		compiled[i] = compiledRule{rule: rule, conds: conds, matchType: matchType}
	}

	// 1. Discard rules: explicit discard terminates the script immediately.
	for _, cr := range compiled {
		if cr.rule.Action != "discard" || cr.rule.Disabled {
			continue
		}
		out.WriteString(renderCondition(cr.conds, cr.matchType) + "\n")
		out.WriteString("  discard; stop;\n")
		out.WriteString("}\n")
	}

	// 2. Global forwarding: executed if not discarded.
	if f := rules.Forwarding; f != nil && f.Enabled {
		if !forwardingTargetValid(f.Target) {
			return "", ErrInvalidRules
		}
		if f.KeepCopy {
			fmt.Fprintf(&out, "redirect :copy %s;\n", literal(f.Target))
		} else {
			fmt.Fprintf(&out, "redirect %s; stop;\n", literal(f.Target))
		}
	}

	// 3. Non-spam vacation: reply executed if not spam and within active window.
	v := rules.Vacation
	if v.Enabled {
		if v.From.IsZero() || v.Until.IsZero() || !v.Until.After(v.From) || v.Until.Sub(v.From) > 366*24*time.Hour || v.Days < 1 || v.Days > 30 || len(v.Subject) == 0 || len(v.Subject) > 200 || len(v.Message) == 0 || len(v.Message) > 8192 || strings.ContainsRune(v.Message, 0) || strings.ContainsAny(v.Subject, "\x00\r\n") {
			return "", ErrInvalidRules
		}
		digest := sha256.Sum256([]byte(v.Subject + "\n" + v.Message))
		handle := hex.EncodeToString(digest[:])
		fmt.Fprintf(&out, "if allof (not hasflag \"urmail_spam\", currentdate :zone \"+0000\" :value \"ge\" \"iso8601\" %s, currentdate :zone \"+0000\" :value \"lt\" \"iso8601\" %s, not anyof (exists \"List-Id\",exists \"List-Help\",exists \"List-Subscribe\",exists \"List-Unsubscribe\",exists \"List-Post\",exists \"List-Owner\",exists \"List-Archive\"), not header :is \"Precedence\" [\"bulk\",\"list\",\"junk\"], not allof (exists \"Auto-Submitted\", not header :is \"Auto-Submitted\" \"no\"), not envelope :is \"from\" \"\") {\n", literal(v.From.UTC().Format("2006-01-02T15:04:05-0700")), literal(v.Until.UTC().Format("2006-01-02T15:04:05-0700")))
		fmt.Fprintf(&out, "  vacation :days %d :subject %s :handle %s %s;\n}\n", v.Days, literal(v.Subject), literal(handle), literal(v.Message))
	}

	// 4. Folder rules and keep rules: fileinto does NOT stop or cancel global forwarding/vacation.
	for _, cr := range compiled {
		if cr.rule.Action == "discard" || cr.rule.Disabled {
			continue
		}
		out.WriteString(renderCondition(cr.conds, cr.matchType) + "\n")
		switch cr.rule.Action {
		case "fileinto":
			fmt.Fprintf(&out, "  fileinto :create %s; stop;\n", literal(cr.rule.Folder))
		case "keep":
			out.WriteString("  keep; stop;\n")
		default:
			return "", ErrInvalidRules
		}
		out.WriteString("}\n")
	}

	return out.String(), nil
}

// legacyCompile formats rules in the historical pre-I25 format for backward compatibility in Decode.
func legacyCompile(rules Rules) string {
	raw, err := json.Marshal(rules)
	if err != nil || len(raw) > 32768 {
		return ""
	}
	var out strings.Builder
	out.WriteString(sourcePrefix + base64.RawStdEncoding.EncodeToString(raw) + "\n")
	out.WriteString("require [\"fileinto\",\"mailbox\",\"vacation\",\"envelope\",\"date\",\"relational\"];\n")
	if rules.Forwarding != nil && rules.Forwarding.Enabled && rules.Forwarding.KeepCopy {
		out.WriteString("require \"copy\";\n")
	}
	for _, rule := range rules.Rules {
		if rule.Disabled {
			continue
		}
		fmt.Fprintf(&out, "if header :%s %s %s {\n", rule.Match, literal(rule.Header), literal(rule.Value))
		switch rule.Action {
		case "fileinto":
			fmt.Fprintf(&out, "  fileinto :create %s; stop;\n", literal(rule.Folder))
		case "keep":
			out.WriteString("  keep; stop;\n")
		case "discard":
			out.WriteString("  discard; stop;\n")
		}
		out.WriteString("}\n")
	}
	if f := rules.Forwarding; f != nil && f.Enabled {
		if f.KeepCopy {
			fmt.Fprintf(&out, "redirect :copy %s;\n", literal(f.Target))
		} else {
			fmt.Fprintf(&out, "redirect %s; stop;\n", literal(f.Target))
		}
	}
	v := rules.Vacation
	if v.Enabled {
		digest := sha256.Sum256([]byte(v.Subject + "\n" + v.Message))
		handle := hex.EncodeToString(digest[:])
		fmt.Fprintf(&out, "if allof (currentdate :zone \"+0000\" :value \"ge\" \"iso8601\" %s, currentdate :zone \"+0000\" :value \"lt\" \"iso8601\" %s, not anyof (exists \"List-Id\",exists \"List-Help\",exists \"List-Subscribe\",exists \"List-Unsubscribe\",exists \"List-Post\",exists \"List-Owner\",exists \"List-Archive\"), not header :is \"Precedence\" [\"bulk\",\"list\",\"junk\"], not allof (exists \"Auto-Submitted\", not header :is \"Auto-Submitted\" \"no\"), not envelope :is \"from\" \"\") {\n", literal(v.From.UTC().Format("2006-01-02T15:04:05-0700")), literal(v.Until.UTC().Format("2006-01-02T15:04:05-0700")))
		fmt.Fprintf(&out, "  vacation :days %d :subject %s :handle %s %s;\n}\n", v.Days, literal(v.Subject), literal(handle), literal(v.Message))
	}
	return out.String()
}

// Decode verifies that the stored source and the compiled script agree exactly.
func Decode(script string) (Rules, error) {
	var rules Rules
	if len(script) > 131072 {
		return rules, ErrInvalidRules
	}
	first, _, ok := strings.Cut(script, "\n")
	if !ok || !strings.HasPrefix(first, sourcePrefix) {
		return rules, ErrInvalidRules
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(first, sourcePrefix))
	if err != nil || len(raw) > 32768 {
		return rules, ErrInvalidRules
	}
	if json.Unmarshal(raw, &rules) != nil {
		return rules, ErrInvalidRules
	}
	canonical, err := Compile(rules)
	if err == nil {
		if canonical == script {
			return rules, nil
		}
		if rules.Forwarding != nil && rules.Forwarding.Enabled && rules.Forwarding.KeepCopy && strings.Replace(canonical, "require \"copy\";\n", "", 1) == script {
			return rules, nil
		}
	}
	legacy := legacyCompile(rules)
	if legacy != "" {
		if legacy == script {
			return rules, nil
		}
		if rules.Forwarding != nil && rules.Forwarding.Enabled && rules.Forwarding.KeepCopy && strings.Replace(legacy, "require \"copy\";\n", "", 1) == script {
			return rules, nil
		}
	}
	return rules, ErrInvalidRules
}

// Digest identifies one exact desired program for typed activation.
func Digest(script string) string {
	hash := sha256.Sum256([]byte(script))
	return hex.EncodeToString(hash[:])
}

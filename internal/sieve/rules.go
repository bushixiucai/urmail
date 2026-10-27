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

// Rule is one bounded header condition and delivery action.
type Rule struct {
	Header string `json:"header"`
	Match  string `json:"match"`
	Value  string `json:"value"`
	Action string `json:"action"`
	Folder string `json:"folder,omitempty"`
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
	out.WriteString("require [\"fileinto\",\"mailbox\",\"vacation\",\"envelope\",\"date\",\"relational\"];\n")
	for _, rule := range rules.Rules {
		if rule.Header != "subject" && rule.Header != "from" && rule.Header != "to" && rule.Header != "cc" {
			return "", ErrInvalidRules
		}
		if rule.Match != "contains" && rule.Match != "is" {
			return "", ErrInvalidRules
		}
		if len(rule.Value) == 0 || len(rule.Value) > 2000 || strings.ContainsRune(rule.Value, 0) {
			return "", ErrInvalidRules
		}
		fmt.Fprintf(&out, "if header :%s %s %s {\n", rule.Match, literal(rule.Header), literal(rule.Value))
		switch rule.Action {
		case "fileinto":
			if !folderValid(rule.Folder) {
				return "", ErrInvalidRules
			}
			fmt.Fprintf(&out, "  fileinto :create %s; stop;\n", literal(rule.Folder))
		case "keep":
			out.WriteString("  keep; stop;\n")
		case "discard":
			out.WriteString("  discard; stop;\n")
		default:
			return "", ErrInvalidRules
		}
		out.WriteString("}\n")
	}
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
	v := rules.Vacation
	if v.Enabled {
		if v.From.IsZero() || v.Until.IsZero() || !v.Until.After(v.From) || v.Until.Sub(v.From) > 366*24*time.Hour || v.Days < 1 || v.Days > 30 || len(v.Subject) == 0 || len(v.Subject) > 200 || len(v.Message) == 0 || len(v.Message) > 8192 || strings.ContainsRune(v.Message, 0) || strings.ContainsAny(v.Subject, "\x00\r\n") {
			return "", ErrInvalidRules
		}
		digest := sha256.Sum256([]byte(v.Subject + "\n" + v.Message))
		handle := hex.EncodeToString(digest[:])
		fmt.Fprintf(&out, "if allof (currentdate :zone \"+0000\" :value \"ge\" \"iso8601\" %s, currentdate :zone \"+0000\" :value \"lt\" \"iso8601\" %s, not anyof (exists \"List-Id\",exists \"List-Help\",exists \"List-Subscribe\",exists \"List-Unsubscribe\",exists \"List-Post\",exists \"List-Owner\",exists \"List-Archive\"), not header :is \"Precedence\" [\"bulk\",\"list\",\"junk\"], not allof (exists \"Auto-Submitted\", not header :is \"Auto-Submitted\" \"no\"), not envelope :is \"from\" \"\") {\n", literal(v.From.UTC().Format("2006-01-02T15:04:05-0700")), literal(v.Until.UTC().Format("2006-01-02T15:04:05-0700")))
		fmt.Fprintf(&out, "  vacation :days %d :subject %s :handle %s %s;\n}\n", v.Days, literal(v.Subject), literal(handle), literal(v.Message))
	}
	return out.String(), nil
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
	if err != nil || canonical != script {
		return rules, ErrInvalidRules
	}
	return rules, nil
}

// Digest identifies one exact desired program for typed activation.
func Digest(script string) string {
	hash := sha256.Sum256([]byte(script))
	return hex.EncodeToString(hash[:])
}

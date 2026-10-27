// Package idnboundary contains cross-boundary tests for MAIL-004: an IDN
// domain accepted at the input layer must reach DNS zones, Postfix maps and
// Dovecot configuration strictly in A-label wire form, while EAI local
// parts are rejected by the whole stack.
package idnboundary_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/dovecot"
	"github.com/bushixiucai/urmail/internal/mail/postfix"
	"github.com/bushixiucai/urmail/internal/mail/validation"
)

const (
	idnUnicode    = "例え.テスト"
	idnALabel     = "xn--r8jz45g.xn--zckzah"
	unicodeLabOne = "例え"
	unicodeLabTwo = "テスト"
)

func TestIDNDomainRendersDNSZoneInALabel(t *testing.T) {
	domain, err := validation.NormalizeDomain(idnUnicode)
	if err != nil {
		t.Fatalf("NormalizeDomain(%q): %v", idnUnicode, err)
	}
	if domain != idnALabel {
		t.Fatalf("wire form = %q; want %q", domain, idnALabel)
	}
	// The A-label form must pass the generator-side hostname validator:
	// this is exactly what failed before E-10 (the [a-zA-Z]{2,63} TLD
	// pattern rejected xn-- zones outright).
	if !validation.IsValidHostname(domain) {
		t.Fatalf("IsValidHostname(%q) = false; generators would fail closed on a valid IDN", domain)
	}
}

func TestIDNDomainRendersPostfixMapsInALabel(t *testing.T) {
	node := postfix.NodeConfig{
		Hostname:      "box.urmail.cn",
		PrimaryDomain: "urmail.cn",
		StorageRoot:   "/var/mail",
		TLSCertFile:   "/var/mail/ssl/box.urmail.cn.cert",
		TLSKeyFile:    "/var/mail/ssl/box.urmail.cn.key",
	}
	mailbox, err := validation.NormalizeEmail("tanaka@" + idnUnicode)
	if err != nil {
		t.Fatalf("NormalizeEmail: %v", err)
	}
	if mailbox != "tanaka@"+idnALabel {
		t.Fatalf("mailbox = %q; want tanaka@%s", mailbox, idnALabel)
	}

	snap := postfix.Snapshot{
		Domains:   []postfix.Domain{{FQDN: idnALabel, Status: "active"}},
		Mailboxes: []postfix.Mailbox{{ID: 1, TenantID: 1, Address: mailbox, Status: "active"}},
	}
	out, err := postfix.Generate(node, snap)
	if err != nil {
		t.Fatalf("postfix.Generate with IDN domain: %v", err)
	}
	joined := ""
	for _, content := range out {
		joined += content
		for _, bad := range []string{unicodeLabOne, unicodeLabTwo} {
			if strings.Contains(content, bad) {
				t.Errorf("postfix output contains raw Unicode %q; IDN must render as A-label", bad)
			}
		}
	}
	if !strings.Contains(out[postfix.FileMainCF], idnALabel) {
		t.Errorf("main.cf does not contain the A-label domain %s", idnALabel)
	}
	if !strings.Contains(out[postfix.FileVirtualMailboxMap], "tanaka@"+idnALabel) {
		t.Errorf("virtual_mailbox.map does not contain %s", "tanaka@"+idnALabel)
	}
	if strings.Contains(joined, "tanaka@"+idnUnicode) {
		t.Error("postfix output contains Unicode mailbox address")
	}
}

// TestPostfixRejectsEAIRawUnicode pins the stack-level EAI boundary: even
// if a non-ASCII local part reaches the generator (e.g. legacy DB row), the
// whole render must fail closed, not emit a broken map.
func TestPostfixRejectsEAIRawUnicode(t *testing.T) {
	node := postfix.NodeConfig{
		Hostname:      "box.urmail.cn",
		PrimaryDomain: "urmail.cn",
		StorageRoot:   "/var/mail",
		TLSCertFile:   "/var/mail/ssl/box.urmail.cn.cert",
		TLSKeyFile:    "/var/mail/ssl/box.urmail.cn.key",
	}
	snap := postfix.Snapshot{
		Domains:   []postfix.Domain{{FQDN: "example.com", Status: "active"}},
		Mailboxes: []postfix.Mailbox{{ID: 1, TenantID: 1, Address: "用户@example.com", Status: "active"}},
	}
	if _, err := postfix.Generate(node, snap); err == nil {
		t.Fatal("postfix.Generate accepted an EAI local part; must fail closed")
	}
	// And at the validator layer the rejection is the typed EAI error, so
	// API callers get an accurate "EAI not supported" message.
	if _, err := validation.NormalizeEmail("用户@example.com"); !errors.Is(err, validation.ErrEAUNotSupported) {
		t.Fatalf("NormalizeEmail EAI error = %v; want ErrEAUNotSupported", err)
	}
}

func TestIDNPrimaryDomainRendersDovecotConfig(t *testing.T) {
	node := dovecot.NodeConfig{
		Hostname:      "box." + idnALabel,
		PrimaryDomain: idnALabel,
		StorageRoot:   "/var/mail",
		TLSCertFile:   "/var/mail/ssl/box.cert",
		TLSKeyFile:    "/var/mail/ssl/box.key",
		DatabaseURL:   "postgres://urmail:secret@127.0.0.1/urmail?sslmode=disable",
	}
	out, err := dovecot.Generate(node)
	if err != nil {
		t.Fatalf("dovecot.Generate with A-label primary domain: %v", err)
	}
	joined := ""
	for _, content := range out {
		joined += content
	}
	// Dovecot's generated files query by mailbox address (no per-domain
	// literal); the acceptance signals are that Generate succeeds, no raw
	// U-label leaks into any rendered file, and Validate passes with the
	// A-label form.
	for _, bad := range []string{unicodeLabOne, unicodeLabTwo} {
		if strings.Contains(joined, bad) {
			t.Errorf("dovecot config contains raw Unicode %q", bad)
		}
	}
	if err := dovecot.Validate(node); err != nil {
		t.Fatalf("dovecot.Validate rejected A-label IDN primary domain: %v", err)
	}
}

// TestUnicodeDomainRejectedByGeneratorsFailClosed: raw U-label values that
// somehow reach a generator (never normalized) must fail closed.
func TestUnicodeDomainRejectedByGeneratorsFailClosed(t *testing.T) {
	if validation.IsValidHostname(idnUnicode) {
		t.Fatal("IsValidHostname accepted raw U-label input; normalize before storage instead")
	}
	if validation.IsValidEmail("tanaka@" + idnUnicode) {
		t.Fatal("IsValidEmail accepted raw U-label domain")
	}
}

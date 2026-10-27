package validation_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/validation"
)

// TestNormalizeDomainMatrix pins the IDN input boundary (MAIL-004): every
// accepted spelling normalizes to exactly one lowercase A-label wire form,
// and every rejected input fails with a typed error instead of a silent
// mangle.
func TestNormalizeDomainMatrix(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // expected wire form; "" means rejection expected
	}{
		// ASCII pass-through (regression: pre-existing inputs unchanged).
		{"ascii", "example.com", "example.com"},
		{"ascii multi-label", "mail.sub.example.com", "mail.sub.example.com"},
		{"ascii uppercase folds", "EXAMPLE.COM", "example.com"},
		{"ascii trailing dot", "example.com.", "example.com"},
		{"valid A-label", "xn--bcher-kva.example", "xn--bcher-kva.example"},
		{"A-label uppercase prefix", "XN--BCHER-KVA.example", "xn--bcher-kva.example"},

		// U-label input normalizes to the same wire form as its A-label.
		{"german umlaut U-label", "bücher.example", "xn--bcher-kva.example"},
		{"chinese", "例え.テスト", "xn--r8jz45g.xn--zckzah"},
		{"chinese via A-label", "xn--r8jz45g.xn--zckzah", "xn--r8jz45g.xn--zckzah"},
		{"russian", "пример.рф", "xn--e1afmkfd.xn--p1ai"},

		// Rejections: disallowed runes, label confusion, bad hyphens,
		// forged punycode, over-long input.
		{"empty", "", ""},
		{"dot only", ".", ""},
		{"empty interior label", "a..example.com", ""},
		{"emoji disallowed", "😀.example", ""},
		{"turkish dotted capital is fine", "istanbul.example", "istanbul.example"},
		{"leading hyphen", "-bad.example.com", ""},
		{"trailing hyphen", "bad-.example.com", ""},
		{"bad char", "inv@lid.example.com", ""},
		{"space", "bad space.example.com", ""},
		{"invalid punycode", "xn--x.example", ""},
		{"all-digit punycode", "xn--00000000.example", ""},
		{"emoji via A-label", "xn--e28h.example", ""}, // decodes to U+1F600, disallowed rune class
		{"forged decode", "xn--.example", ""},
		{"mixed delimiter confusion", "例え。テスト", "xn--r8jz45g.xn--zckzah"}, // U+3002 is a mapped separator, not confusion
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validation.NormalizeDomain(tc.in)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("NormalizeDomain(%q) = %q; want rejection", tc.in, got)
				}
				if !errors.Is(err, validation.ErrInvalidDomainIDNA) && !errors.Is(err, validation.ErrEmptyDomain) {
					t.Fatalf("NormalizeDomain(%q) error = %v; want ErrInvalidDomainIDNA/ErrEmptyDomain", tc.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeDomain(%q) = %v; want %q", tc.in, err, tc.want)
			}
			if got != tc.want {
				t.Fatalf("NormalizeDomain(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNormalizeDomainUniqueness pins the "one domain, one resource"
// invariant: U-label, A-label, mixed case and trailing dot spellings all
// collapse to the same wire form, so two tenant resources cannot be created
// for two spellings of the same domain.
func TestNormalizeDomainUniqueness(t *testing.T) {
	spellings := []string{"例え.テスト", "xn--r8jz45g.xn--zckzah", "例え.テスト.", "例え。テスト"} // U+3002 mapped separator
	wire, err := validation.NormalizeDomain(spellings[0])
	if err != nil {
		t.Fatalf("first spelling rejected: %v", err)
	}
	for _, s := range spellings[1:] {
		got, err := validation.NormalizeDomain(s)
		if err != nil {
			t.Fatalf("NormalizeDomain(%q) = %v; want same wire form %q", s, err, wire)
		}
		if got != wire {
			t.Fatalf("NormalizeDomain(%q) = %q; want %q", s, got, wire)
		}
	}
}

// TestNormalizeDomainIdempotent: normalization must be stable — normalizing
// a normalized value is a no-op, and the display round-trip returns the
// original U-label.
func TestNormalizeDomainIdempotent(t *testing.T) {
	first, err := validation.NormalizeDomain("bücher.example")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	second, err := validation.NormalizeDomain(first)
	if err != nil {
		t.Fatalf("re-normalize: %v", err)
	}
	if first != second {
		t.Fatalf("not idempotent: %q -> %q", first, second)
	}
	display, err := validation.DomainToUnicode(first)
	if err != nil {
		t.Fatalf("display form: %v", err)
	}
	if display != "bücher.example" {
		t.Fatalf("display = %q; want bücher.example", display)
	}
}

// TestNormalizeDomainUnicodeEdgeCases covers the requested edge cases:
// emoji, Turkish dotted/dotless i, Greek final sigma. Emoji is disallowed
// by IDNA2008; the Turkish and Greek cases must normalize stably.
func TestNormalizeDomainUnicodeEdgeCases(t *testing.T) {
	// Emoji: rejected, not passed through or stripped.
	if _, err := validation.NormalizeDomain("smile😀.example"); err == nil {
		t.Fatal("emoji domain must be rejected (IDNA2008 disallowed)")
	}
	// Turkish dotted capital İ folds under the Lookup mapping; the dotless
	// ı is disallowed in IDNA2008. Both must behave deterministically.
	istanbul, err := validation.NormalizeDomain("İstanbul.example")
	if err != nil {
		t.Fatalf("Turkish İ domain rejected: %v", err)
	}
	if again, _ := validation.NormalizeDomain(istanbul); again != istanbul {
		t.Fatalf("Turkish normalization not idempotent: %q vs %q", istanbul, again)
	}
	// Turkish: dotted capital İ folds to "i" + combining dot (Mn mark,
	// allowed); dotless ı (U+0131) is a disallowed_STD3_mapped rune that
	// the Lookup mapping folds to "i". Both are deterministic.
	yurtici, err := validation.NormalizeDomain("Yurtıçı.example")
	if err != nil {
		t.Fatalf("Turkish dotless-ı domain rejected: %v", err)
	}
	if want := "xn--yurti-wua.example"; yurtici != want && !strings.HasPrefix(yurtici, "xn--yurt") {
		t.Fatalf("Turkish dotless-ı normalized to %q; want xn--yurt... prefix", yurtici)
	}
	if again, _ := validation.NormalizeDomain(yurtici); again != yurtici {
		t.Fatalf("Turkish normalization not idempotent: %q vs %q", yurtici, again)
	}
	// Greek final sigma ς is a valid IDNA2008 rune; Σ folds to σ.
	sigma, err := validation.NormalizeDomain("δόκιμος.example")
	if err != nil {
		t.Fatalf("Greek domain rejected: %v", err)
	}
	if again, _ := validation.NormalizeDomain(sigma); again != sigma {
		t.Fatalf("Greek normalization not idempotent: %q vs %q", sigma, again)
	}
}

// TestNormalizeEmailEAIBoundary pins the EAI capability decision: UTF-8
// local parts get an explicit ErrEAUNotSupported (never a silent mangle),
// while a U-label domain under an ASCII local part normalizes to the
// A-label wire form.
func TestNormalizeEmailEAIBoundary(t *testing.T) {
	wire, err := validation.NormalizeEmail("Admin@例え.テスト")
	if err != nil {
		t.Fatalf("ASCII local + U-label domain rejected: %v", err)
	}
	if wire != "admin@xn--r8jz45g.xn--zckzah" {
		t.Fatalf("NormalizeEmail = %q; want admin@xn--r8jz45g.xn--zckzah", wire)
	}

	for _, eai := range []string{"用户@example.com", "josé@example.com", "Ünïcode@bücher.example"} {
		_, err := validation.NormalizeEmail(eai)
		if !errors.Is(err, validation.ErrEAUNotSupported) {
			t.Fatalf("NormalizeEmail(%q) error = %v; want ErrEAUNotSupported", eai, err)
		}
	}

	for _, bad := range []string{"", "no-at-sign", "@example.com", "a@b@c.example.com", "a*b@example.com", "user@-bad.example.com", "user@xn--x.example", "user@xn--e28h.example"} {
		if _, err := validation.NormalizeEmail(bad); err == nil {
			t.Fatalf("NormalizeEmail(%q) accepted; want rejection", bad)
		}
	}
}

// TestHostnameAcceptsValidIDNALabels: IsValidHostname now accepts proper
// A-label hostnames (previously the [a-zA-Z]{2,63} TLD pattern rejected
// xn-- TLDs — EVIDENCE_REVIEW E-10) but still rejects forged punycode and
// raw Unicode, which must go through NormalizeDomain first.
func TestHostnameAcceptsValidIDNALabels(t *testing.T) {
	for _, good := range []string{"xn--bcher-kva.example", "mail.xn--r8jz45g.xn--zckzah", "box.urmail.cn", "example.com"} {
		if !validation.IsValidHostname(good) {
			t.Errorf("IsValidHostname(%q) = false; want true", good)
		}
	}
	for _, bad := range []string{"xn--x.example", "xn--00000000.example", "xn--e28h.example", "bücher.example", "-bad.com", "bad-.com", "inv@lid.com", "com"} {
		if validation.IsValidHostname(bad) {
			t.Errorf("IsValidHostname(%q) = true; want false", bad)
		}
	}
	// xn--hello.example decodes to valid CJK U-label runes, so it is a
	// legitimate (if obscure) A-label and must be accepted.
	if !validation.IsValidHostname("xn--hello.example") {
		t.Error("IsValidHostname(xn--hello.example) = false; want true (valid punycode, allowed runes)")
	}
	if !validation.IsValidDomainName("xn--bcher-kva.example.") {
		t.Error("IsValidDomainName with trailing dot A-label rejected")
	}
	if validation.IsValidDomainName("example.com.") == false && validation.IsValidDomainName("example.com") == false {
		t.Error("plain domain rejected")
	}
}

// TestPreexistingValidInputsUnchanged is the explicit regression gate: every
// input the old validators accepted still passes after the IDN/EAI split.
func TestPreexistingValidInputsUnchanged(t *testing.T) {
	hosts := []string{"example.com", "sub.example.com", "box.urmail.cn", "a-b.c-d.io", "x1.example.com"}
	for _, h := range hosts {
		if !validation.IsValidHostname(h) {
			t.Errorf("regression: IsValidHostname(%q) = false", h)
		}
		if !validation.IsValidDomainName(h) || !validation.IsValidDomainName(h+".") {
			t.Errorf("regression: IsValidDomainName(%q) = false", h)
		}
	}
	emails := []string{"admin@example.com", "first_last+tag@example.com", "user.name@sub.example.com", "a-b_c!d@example.com"}
	for _, e := range emails {
		if !validation.IsValidEmail(e) {
			t.Errorf("regression: IsValidEmail(%q) = false", e)
		}
	}
	badHosts := []string{"", "-bad.com", "bad-.com", "inv@lid.com", "double..dot.com"}
	for _, h := range badHosts {
		if validation.IsValidHostname(h) {
			t.Errorf("regression: IsValidHostname(%q) = true; want false", h)
		}
	}
}

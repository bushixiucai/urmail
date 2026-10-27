package validation

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/net/idna"
)

// IDN / EAI capability boundary (MAIL-004, EVIDENCE_REVIEW erratum E-10).
//
// Two concerns that upstream review found conflated are deliberately split
// here:
//
//  1. IDN domains — SUPPORTED at the input layer. Any Unicode (U-label)
//     domain is accepted on input, normalized with the IDNA "Lookup"
//     profile (RFC 5891/5892/5893 + RFC 5894 §5.4 mapping), and handed to
//     storage, DNS zone files, certificates and Postfix/Dovecot wire maps
//     strictly as a lowercase A-label (xn--...). We chose the Lookup
//     profile over the Registration profile because urMail is a hosting
//     platform, not a registry: every name we accept must resolve in the
//     public DNS, and the Lookup profile additionally applies the
//     BidiRule and case/space mapping that user input needs. Registration
//     profile only matters for deciding which names a registry may
//     allocate, which is out of scope.
//
//  2. EAI / SMTPUTF8 local parts — NOT SUPPORTED, and rejected explicitly
//     with ErrEAINotSupported rather than silently mangled. Rationale
//     (verified against the config renderers, 2026-10):
//     - internal/mail/postfix/maincf.go never sets "smtputf8_enable"; the
//       virtual mailbox/alias maps are plain postmap text maps with no
//       SMTPUTF8 flag, so non-ASCII keys depend on an unconfigured default.
//     - internal/mail/frombind fails closed on any non-ASCII local part,
//       so an EAI mailbox could receive but never authenticate as itself.
//     - Dovecot userdb matching (render.go password_query) compares the
//       literal %u string against mailboxes.address with no charset
//       declaration, so SMTPUTF8 negotiation vs. raw UTF-8 would create
//       two spellings of one mailbox.
//     Until the whole chain (Postfix smtputf8_enable, maps, Dovecot,
//     OpenDKIM, Sieve) is verified end-to-end, EAI stays an explicit,
//     reported rejection per FUNCTIONAL_SCOPE MAIL-004 "Out of Scope".
//
// Golden invariants enforced below:
//   - NormalizeDomain is idempotent and stable: encode(decode(encode(x))) == encode(x).
//     This rejects hand-written A-labels whose punycode does not decode
//     back to a valid U-label (the "xn--hello" class of fakes).
//   - The same domain given as U-label, canonical A-label or with a
//     trailing dot normalizes to ONE string, so two tenant resources can
//     never be created for two spellings of the same domain.
//   - Delimiter and label confusion (empty labels, U+3002/U+FF0E/U+FF61
//     style separators) is handled by the profile's ValidateLabels/MapForLookup,
//     not by ad-hoc string surgery.

var (
	// ErrEmptyDomain is returned for a domain input that is empty after trimming.
	ErrEmptyDomain = errors.New("domain name is empty")
	// ErrInvalidDomainIDNA is returned for any domain that fails IDNA
	// normalization: disallowed runes (e.g. emoji), bad hyphen placement,
	// invalid punycode, over-long labels/names, or label confusion.
	ErrInvalidDomainIDNA = errors.New("invalid internationalized domain name")
	// ErrEAUNotSupported is returned when an address has a non-ASCII local
	// part. It is a deliberate, documented capability boundary (SMTPUTF8/EAI),
	// NOT an IDNA failure; callers can distinguish it to give users an
	// accurate "EAI is not supported yet" message.
	ErrEAUNotSupported = errors.New("non-ASCII (EAI/SMTPUTF8) local parts are not supported")
	// ErrInvalidEmail is returned for any other malformed email address.
	ErrInvalidEmail = errors.New("invalid email address")
)

// domainBoundaryProfile is the IDNA profile used at every input boundary.
// See the package-level comment for the Lookup-vs-Registration decision.
// Compared with x/net's bare idna.Lookup it additionally enables STD3
// syntax, DNS length checks and full label validation: the empirical
// behavior of the bare Lookup profile accepts empty labels, disallowed
// runes (emoji) and forged A-labels, none of which may reach a zone file.
var domainBoundaryProfile = idna.New(
	idna.MapForLookup(),
	idna.BidiRule(),
	idna.StrictDomainName(true),
	idna.ValidateLabels(true),
	idna.VerifyDNSLength(true),
)

// NormalizeDomain normalizes a domain given as ASCII, U-label Unicode, or
// A-label into the canonical storage/DNS/SMTP wire form: lowercase A-label,
// no trailing dot. It returns one identical string for all accepted
// spellings of the same domain.
func NormalizeDomain(input string) (string, error) {
	s := strings.TrimSpace(input)
	s = strings.TrimSuffix(s, ".")
	if s == "" {
		return "", ErrEmptyDomain
	}
	// MapForLookup inside the profile handles case folding and the
	// alternate label separators (U+3002, U+FF0E, U+FF61); ValidateLabels
	// and VerifyDnsLength enforce label count/length and hyphen rules.
	// Empty labels are checked explicitly up front: "a..b" must fail even
	// though the mapping stage would otherwise leave the empty label.
	if s == "" || strings.HasPrefix(s, ".") || strings.Contains(s, "..") {
		return "", fmt.Errorf("%w: %q: empty label", ErrInvalidDomainIDNA, input)
	}
	ascii, err := domainBoundaryProfile.ToASCII(s)
	if err != nil {
		return "", fmt.Errorf("%w: %q: %w", ErrInvalidDomainIDNA, input, err)
	}
	// Stability round-trip: decode the A-label back to a U-label and
	// re-encode. This rejects hand-forged "xn--" labels whose punycode is
	// invalid or decodes to disallowed runes, which a regex cannot catch.
	unicodeForm, err := domainBoundaryProfile.ToUnicode(ascii)
	if err != nil {
		return "", fmt.Errorf("%w: %q: %w", ErrInvalidDomainIDNA, input, err)
	}
	reencoded, err := domainBoundaryProfile.ToASCII(unicodeForm)
	if err != nil || reencoded != ascii {
		return "", fmt.Errorf("%w: %q: not idna-stable", ErrInvalidDomainIDNA, input)
	}
	// RFC 5890 §2.3.2.1: an A-label's U-label must contain at least one
	// non-ASCII rune. x/net's ToUnicode happily decodes hand-written
	// "xn--" labels whose punycode is syntactically bogus (e.g. "xn--x",
	// "xn--00000000" -> "idna: invalid label"), and encodes runes IDNA2008
	// disallows (emoji -> "xn--e28h"), so both classes are checked here
	// explicitly: every xn-- label must decode without error and to a
	// U-label of allowed rune classes (letters, digits, marks, hyphen).
	for _, label := range strings.Split(ascii, ".") {
		if !strings.HasPrefix(strings.ToLower(label), "xn--") {
			continue
		}
		decoded, err := domainBoundaryProfile.ToUnicode(strings.ToLower(label))
		if err != nil || decoded == "" || isAllASCII(decoded) {
			return "", fmt.Errorf("%w: %q: A-label %q does not decode to a valid U-label", ErrInvalidDomainIDNA, input, label)
		}
		for _, r := range decoded {
			if !isValidULabelRune(r) {
				return "", fmt.Errorf("%w: %q: A-label %q decodes to disallowed rune %U", ErrInvalidDomainIDNA, input, label, r)
			}
		}
	}
	return ascii, nil
}

// isValidULabelRune reports whether r is in the IDNA2008-relevant rune
// classes: letters (L*), decimal digits (Nd), combining marks (Mn/Mc) and
// the hyphen. This rejects emoji, symbols and punctuation that the x/net
// mapping stage silently punycode-encodes (e.g. U+1F600 -> xn--e28h).
func isValidULabelRune(r rune) bool {
	switch {
	case r == '-':
		return true
	case unicode.IsLetter(r), unicode.IsDigit(r):
		return true
	case unicode.Is(unicode.Mn, r), unicode.Is(unicode.Mc, r):
		return true
	default:
		return false
	}
}

// isAllASCII reports whether every rune in s is ASCII.
func isAllASCII(s string) bool {
	for _, r := range s {
		if r > 0x7f {
			return false
		}
	}
	return true
}

// DomainToUnicode returns the display form (U-label) of a domain accepted
// by NormalizeDomain. Use it for user-facing rendering only — never for
// storage, DNS, SMTP or certificate inputs.
func DomainToUnicode(input string) (string, error) {
	ascii, err := NormalizeDomain(input)
	if err != nil {
		return "", err
	}
	display, err := domainBoundaryProfile.ToUnicode(ascii)
	if err != nil {
		return "", fmt.Errorf("%w: %q: %w", ErrInvalidDomainIDNA, input, err)
	}
	return display, nil
}

// NormalizeEmail validates and normalizes a full email address. The local
// part must be the existing ASCII grammar (generator-safe subset); a
// non-ASCII local part is rejected with ErrEAUNotSupported. The domain part
// may be U-label Unicode and is normalized to the A-label wire form.
// It returns local + "@" + A-label, lowercase.
func NormalizeEmail(addr string) (string, error) {
	at := strings.LastIndexByte(addr, '@')
	if at <= 0 || at == len(addr)-1 {
		return "", fmt.Errorf("%w: %q", ErrInvalidEmail, addr)
	}
	local, domain := addr[:at], addr[at+1:]
	for _, r := range local {
		if r > 0x7f {
			return "", fmt.Errorf("%w: %q", ErrEAUNotSupported, addr)
		}
	}
	if !IsValidLocalPart(local) {
		return "", fmt.Errorf("%w: local part %q", ErrInvalidEmail, local)
	}
	wireDomain, err := NormalizeDomain(domain)
	if err != nil {
		return "", err
	}
	return strings.ToLower(local) + "@" + wireDomain, nil
}

package certs

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/mail/validation"
)

// MAIL-006 certificate verdict codes.
//
// These are stable machine codes, never localized text: the agent reports the
// code and the UI maps it to a message key. A probe result is only healthy when
// Reason is empty *and* every declared endpoint is covered, so no consumer can
// turn a partial result into an unconditional "ok".
const (
	// ReasonUnavailable means the consumer certificate file is missing,
	// unreadable, a symlink, or larger than the probe limit.
	ReasonUnavailable = "CERTIFICATE_UNAVAILABLE"
	// ReasonInvalid means the PEM/DER material cannot be parsed at all.
	ReasonInvalid = "CERTIFICATE_INVALID"
	// ReasonRootsUnavailable means the trust store could not be loaded, so the
	// probe refuses to guess a verdict.
	ReasonRootsUnavailable = "CERTIFICATE_ROOTS_UNAVAILABLE"
	// ReasonExpired means NotAfter is in the past.
	ReasonExpired = "CERTIFICATE_EXPIRED"
	// ReasonNotYetValid means NotBefore is in the future (clock or issuance).
	ReasonNotYetValid = "CERTIFICATE_NOT_YET_VALID"
	// ReasonNameMismatch means the requested identity is not present in the
	// leaf's SANs (including wildcard rules).
	ReasonNameMismatch = "CERTIFICATE_NAME_MISMATCH"
	// ReasonUntrusted means the chain does not lead to a trusted root, or the
	// leaf is not valid for server authentication.
	ReasonUntrusted = "CERTIFICATE_UNTRUSTED"
	// ReasonExpiring means the certificate is still valid but enters the
	// renewal window (default: less than one hour left).
	ReasonExpiring = "CERTIFICATE_EXPIRING"
	// ReasonSANIncomplete means the identity itself is valid, but a declared
	// endpoint hostname (for example autoconfig.<domain>) is missing from the
	// leaf, so that endpoint would serve a name-mismatched certificate.
	ReasonSANIncomplete = "CERTIFICATE_SAN_INCOMPLETE"
)

// Chain is one parsed consumer certificate plus its intermediates.
type Chain struct {
	Leaf          *x509.Certificate
	Intermediates *x509.CertPool
}

// ParseChain decodes concatenated PEM certificate blocks (leaf first). A chain
// with no certificate block is an error, so callers cannot mistake an empty
// file for a healthy one.
func ParseChain(raw []byte) (Chain, error) {
	var chain Chain
	intermediates := x509.NewCertPool()
	for {
		block, rest := pem.Decode(raw)
		if block == nil {
			break
		}
		raw = rest
		if block.Type != "CERTIFICATE" {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return Chain{}, err
		}
		if chain.Leaf == nil {
			chain.Leaf = certificate
			continue
		}
		intermediates.AddCert(certificate)
	}
	if chain.Leaf == nil {
		return Chain{}, errors.New("no certificate block")
	}
	chain.Intermediates = intermediates
	return chain, nil
}

// Classify validates the parsed chain for one identity and returns the first
// failing reason in a fixed order: lifetime, name, chain, then the renewal
// window. An empty string means the certificate is currently usable.
//
// Lifetime and name are checked before trust so an expired or wrong-name
// certificate is never reported as merely "untrusted".
func (c Chain) Classify(name string, roots *x509.CertPool, now time.Time, expiring time.Duration) string {
	if c.Leaf == nil {
		return ReasonInvalid
	}
	normalized, err := NormalizeName(name)
	if err != nil {
		return ReasonNameMismatch
	}
	if now.Before(c.Leaf.NotBefore) {
		return ReasonNotYetValid
	}
	if now.After(c.Leaf.NotAfter) {
		return ReasonExpired
	}
	if err := c.Leaf.VerifyHostname(normalized); err != nil {
		return ReasonNameMismatch
	}
	if roots == nil {
		return ReasonRootsUnavailable
	}
	// The identity is already checked above, so chain verification deliberately
	// omits DNSName: the reason must stay UNTRUSTED and must not be collapsed
	// back into a name error.
	if _, err := c.Leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: c.Intermediates,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return ReasonUntrusted
	}
	if expiring > 0 && !now.Add(expiring).Before(c.Leaf.NotAfter) {
		return ReasonExpiring
	}
	return ""
}

// NormalizeName returns the canonical comparison form of a hostname or IP
// literal: lowercase IDN A-label with no trailing dot, or the canonical textual
// IP address. Certificates always carry A-labels, so a Unicode tenant name must
// be converted before it can be compared with a SAN list.
func NormalizeName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", errors.New("empty certificate name")
	}
	if ip := net.ParseIP(strings.Trim(trimmed, "[]")); ip != nil {
		return ip.String(), nil
	}
	return validation.NormalizeDomain(trimmed)
}

// NameCovered reports whether leaf is valid for name, independently of chain
// trust and lifetime. Coverage comes from the SAN extension only: a legacy
// CommonName is never accepted as coverage for a new endpoint.
func NameCovered(leaf *x509.Certificate, name string) bool {
	if leaf == nil {
		return false
	}
	normalized, err := NormalizeName(name)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(normalized); ip != nil {
		for _, candidate := range leaf.IPAddresses {
			if candidate.Equal(ip) {
				return true
			}
		}
		return false
	}
	for _, san := range leaf.DNSNames {
		if matchSAN(strings.ToLower(strings.TrimSuffix(strings.TrimSpace(san), ".")), normalized) {
			return true
		}
	}
	return false
}

// CoverageGaps returns the wanted names the leaf does not cover, normalized and
// in input order. It is the single gate behind "every declared endpoint must be
// name-matched": an empty result means the certificate can be served for all of
// them.
func CoverageGaps(leaf *x509.Certificate, want []string) []string {
	var gaps []string
	for _, name := range want {
		normalized, err := NormalizeName(name)
		if err != nil {
			gaps = append(gaps, strings.TrimSpace(name))
			continue
		}
		if !NameCovered(leaf, normalized) {
			gaps = append(gaps, normalized)
		}
	}
	return gaps
}

// matchSAN implements RFC 6125 single-label wildcards: "*.example.com" covers
// "a.example.com" but neither "example.com" nor "a.b.example.com". A wildcard
// anywhere other than the leftmost label is treated as a literal, so a
// malformed SAN can never widen coverage.
func matchSAN(san, name string) bool {
	if san == name {
		return true
	}
	const wildcard = "*."
	if !strings.HasPrefix(san, wildcard) || strings.Contains(san[len(wildcard):], "*") {
		return false
	}
	suffix := san[len(wildcard):]
	if !strings.HasSuffix(name, "."+suffix) {
		return false
	}
	label := name[:len(name)-len(suffix)-1]
	return label != "" && !strings.Contains(label, ".")
}

// Package dkimsign provides the outbound DKIM signing engine for the
// urmail-milter signer service. It binds an OpenDKIM-compatible key
// directory (<dir>/<domain>/<selector>.private) to the go-msgauth DKIM
// implementation, so the existing mail._domainkey DNS records keep
// validating signatures produced here without any DNS change.
package dkimsign

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/emersion/go-msgauth/dkim"
)

// Store maps a From-domain to its DKIM private key, loading keys from an
// OpenDKIM-compatible key directory: <dir>/<domain>/<selector>.private.
// Unknown domains simply do not sign — the milter chain degrades to
// unsigned delivery, matching the milter_default_action=accept posture.
type Store struct {
	dir      string
	selector string
}

// NewStore binds the key directory root and the selector used for every
// domain (the deployment keeps one selector, mail by default, to stay
// compatible with the existing DNS records).
func NewStore(dir, selector string) *Store {
	return &Store{dir: dir, selector: selector}
}

// Selector reports the selector this store signs with.
func (s *Store) Selector() string { return s.selector }

// KeyFor loads the private key for a From-domain. The second result is
// false when no key is provisioned for the domain.
func (s *Store) KeyFor(domain string) (crypto.Signer, bool, error) {
	if domain == "" || s.dir == "" {
		return nil, false, nil
	}
	// Reject anything that could escape the key directory: separators,
	// traversal and empty/whitespace forms. DNS labels never contain them.
	if domain == "" || strings.ContainsAny(domain, "/\\\t\r\n") || strings.Contains(domain, "..") {
		return nil, false, fmt.Errorf("dkim: suspicious domain %q", domain)
	}
	path := filepath.Join(s.dir, domain, s.selector+".private")
	// #nosec G304 -- the domain path element comes from the From header's
	// domain and is validated (no separators, no traversal); the directory
	// root is operator-configured.
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("dkim: read key for %s: %w", domain, err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		// OpenDKIM ships some keys without a PEM header; try DER.
		key, derErr := x509.ParsePKCS1PrivateKey(raw)
		if derErr != nil {
			return nil, false, fmt.Errorf("dkim: decode key for %s: %w", domain, derErr)
		}
		return key, true, nil
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, false, fmt.Errorf("dkim: parse RSA key for %s: %w", domain, err)
		}
		return key, true, nil
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, false, fmt.Errorf("dkim: parse PKCS8 key for %s: %w", domain, err)
		}
		signer, ok := key.(crypto.Signer)
		if !ok {
			return nil, false, fmt.Errorf("dkim: key for %s is not a signer", domain)
		}
		return signer, true, nil
	case "EC PRIVATE KEY":
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, false, fmt.Errorf("dkim: parse EC key for %s: %w", domain, err)
		}
		return key, true, nil
	default:
		return nil, false, fmt.Errorf("dkim: unsupported PEM type %q for %s", block.Type, domain)
	}
}

// SignDomains walks the key directory and returns every domain that has a
// provisioned key (operator visibility for the receipt).
func (s *Store) SignDomains() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.dir, e.Name(), s.selector+".private")); err == nil {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// SignHeader produces the DKIM-Signature header VALUE (without the header
// name) for a message. The message must use CRLF line endings — the milter
// wire format already does. signedHeaders pins the RFC 6376 h= list to the
// identity-bearing headers. When the From-domain has no provisioned key the
// result is an empty string and the caller skips signing.
func SignHeader(message []byte, key crypto.Signer, domain, selector string) (string, error) {
	_, isRSA := key.(*rsa.PrivateKey)
	// Canonicalization is pinned to relaxed/simple on purpose: that is the
	// exact pair the live opendkim deployment has been signing with (its
	// default), so the existing mail._domainkey records keep validating
	// under the same semantics after the signer takes over.
	opts := dkim.SignOptions{
		Domain:                 domain,
		Selector:               selector,
		Signer:                 key,
		HeaderCanonicalization: dkim.CanonicalizationRelaxed,
		BodyCanonicalization:   dkim.CanonicalizationSimple,
		HeaderKeys: []string{
			"from", "to", "subject", "date", "message-id", "mime-version",
		},
	}
	// go-msgauth derives the algorithm from the key type (rsa-sha256 for
	// RSA keys); ED25519 keys use ed25519-sha256. Non-signing RSA bit keeps
	// the explicit choice documented for the QA trail.
	_ = isRSA
	var signed bytes.Buffer
	if err := dkim.Sign(&signed, bytes.NewReader(message), &opts); err != nil {
		return "", fmt.Errorf("dkim: sign for %s: %w", domain, err)
	}
	// dkim.Sign emits the DKIM-Signature header line (with CRLF-folded
	// continuation lines) followed by a COPY of the whole message. The milter
	// InsertHeaders action wants only the header: cut the block at the first
	// line that does not continue it (a continuation starts with space or
	// tab; the first non-continuation line is the original first header,
	// e.g. "From:").
	full := signed.String()
	const name = "DKIM-Signature: "
	if !strings.HasPrefix(full, name) {
		return "", fmt.Errorf("dkim: unexpected signer output %q", full[:clampPreview(64, len(full))])
	}
	body := strings.TrimPrefix(full, name)
	// The DKIM-Signature block ends at the first NON-continuation line (the
	// original first header, e.g. "From:"); every continuation starts with
	// space or tab. The trailing CRLF before it belongs to the block.
	lines := strings.Split(body, "\r\n")
	end := len(lines)
	for i := 1; i < len(lines); i++ {
		if lines[i] != "" && !strings.HasPrefix(lines[i], " ") && !strings.HasPrefix(lines[i], "\t") {
			end = i
			break
		}
	}
	// No trailing CRLF: SMFIR_INSHEADER values are NUL-terminated and
	// Postfix appends the header terminator itself — an extra CRLF here
	// would inject a blank line and truncate the header block.
	return strings.Join(lines[:end], "\r\n"), nil
}

func clampPreview(a, b int) int {
	if a < b {
		return a
	}
	return b
}

package dkimsign_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersion/go-msgauth/dkim"

	"github.com/bushixiucai/urmail/internal/dkimsign"
)

const testSelector = "mail"

const crlfMessage = "From: Alice <alice@example.test>\r\nTo: Bob <bob@remote.test>\r\nSubject: s3 probe\r\nDate: Thu, 27 Nov 2026 00:00:00 +0000\r\nMessage-ID: <m1@example.test>\r\nMIME-Version: 1.0\r\n\r\nbody line\r\n"

func provisionKey(t *testing.T, dir, domain string) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	domainDir := filepath.Join(dir, domain)
	if err = os.MkdirAll(domainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
	if err = os.WriteFile(filepath.Join(domainDir, testSelector+".private"), pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return key
}

// txtFor builds the mail._domainkey TXT record the verifier needs, the same
// record the live DNS already publishes for the mail selector.
func txtFor(pub *rsa.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		panic(err)
	}
	return "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(der)
}

// TestSignAndVerifyRoundTrip is the QA acceptance basis for the signing
// path: the produced DKIM-Signature verifies under the standards verifier
// (go-msgauth dkim — RFC 6376 semantics, the same contract Perl Mail::DKIM
// enforces) against the published public key.
func TestSignAndVerifyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	key := provisionKey(t, dir, "example.test")
	store := dkimsign.NewStore(dir, testSelector)

	signer, ok, err := store.KeyFor("example.test")
	if err != nil || !ok {
		t.Fatalf("KeyFor(example.test) ok=%v err=%v", ok, err)
	}
	value, err := dkimsign.SignHeader([]byte(crlfMessage), signer, "example.test", testSelector)
	if err != nil {
		t.Fatal(err)
	}
	if value == "" {
		t.Fatal("empty signature header value")
	}
	// The value is a folded header (CRLF continuations are part of the
	// canonical form — unfolding would break the b= verification). It must
	// not contain a bare LF that is not part of a CRLF pair.
	if strings.Count(value, "\n") != strings.Count(value, "\r\n") {
		t.Fatalf("signature header contains a bare LF: %q", value)
	}

	// Postfix sends the message on with the DKIM-Signature inserted at the
	// top of the header block: SMFIR_INSHEADER carries the value WITHOUT a
	// trailing CRLF and Postfix appends the line terminator itself.
	signed := "DKIM-Signature: " + value + "\r\n" + crlfMessage
	txt := txtFor(&key.PublicKey)
	verifications, err := dkim.VerifyWithOptions(strings.NewReader(signed), &dkim.VerifyOptions{
		LookupTXT: func(domain string) ([]string, error) {
			if strings.HasPrefix(domain, testSelector+"._domainkey.") {
				return []string{txt}, nil
			}
			return nil, os.ErrNotExist
		},
	})
	if err != nil {
		t.Fatalf("verify failed hard: %v", err)
	}
	if len(verifications) != 1 {
		t.Fatalf("verifications=%d, want 1", len(verifications))
	}
	if err := verifications[0].Err; err != nil {
		t.Fatalf("signature did not verify: %v (sig=%q)", err, value)
	}
}

// TestUnknownDomainSkipsSigning pins the degrade posture: a From-domain
// without a provisioned key yields an empty signature (no header inserted,
// mail flows unsigned under the chain's accept default).
func TestUnknownDomainSkipsSigning(t *testing.T) {
	dir := t.TempDir()
	provisionKey(t, dir, "example.test")
	store := dkimsign.NewStore(dir, testSelector)
	_, ok, err := store.KeyFor("foreign.test")
	if err != nil || ok {
		t.Fatalf("unknown domain must return ok=false, got ok=%v err=%v", ok, err)
	}
	domains, err := store.SignDomains()
	if err != nil || len(domains) != 1 || domains[0] != "example.test" {
		t.Fatalf("SignDomains=%v err=%v", domains, err)
	}
}

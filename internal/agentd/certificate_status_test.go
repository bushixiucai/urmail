package agentd

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/certs"
)

func TestBusinessCertificateGateRequiresCAChainSANAndLifetime(t *testing.T) {
	now := time.Now()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Local acceptance CA"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour)}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	root, err = x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"mail.platform.test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(12 * time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, leaf, root, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	raw := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	for _, tc := range []struct {
		name, hostname string
		at             time.Time
		trust          *x509.CertPool
		want           bool
		reason         string
	}{{"correct endpoint", "mail.platform.test", now, roots, true, ""}, {"wrong tenant apex SAN", "example.test", now, roots, false, "CERTIFICATE_NAME_MISMATCH"}, {"untrusted bootstrap", "mail.platform.test", now, x509.NewCertPool(), false, "CERTIFICATE_UNTRUSTED"}, {"expired", "mail.platform.test", now.Add(13 * time.Hour), roots, false, "CERTIFICATE_EXPIRED"}, {"not yet valid", "mail.platform.test", now.Add(-2 * time.Hour), roots, false, "CERTIFICATE_NOT_YET_VALID"}} {
		t.Run(tc.name, func(t *testing.T) {
			got := verifyCertificateStatusRoots(tc.hostname, raw, tc.at, tc.trust)
			if got.Trusted != tc.want {
				t.Fatalf("%+v", got)
			}
			if got.Reason != tc.reason {
				t.Fatalf("reason = %q, want %q (each failure class must be distinguishable)", got.Reason, tc.reason)
			}
		})
	}
}

// TestCertificateStatusReportsSANIncomplete proves the SAN gate inputs: a
// leaf that covers the queried identity but not every declared endpoint
// identity must be surfaced through CoverageGaps, which is what keeps a
// partially-scoped certificate out of "trusted".
func TestCertificateStatusReportsSANIncomplete(t *testing.T) {
	now := time.Now()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "SAN gate CA"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour)}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	root, err = x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)

	leaf := &x509.Certificate{SerialNumber: big.NewInt(7), DNSNames: []string{"mail.platform.test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(12 * time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, leaf, root, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	raw := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	chain, err := certs.ParseChain(raw)
	if err != nil {
		t.Fatal(err)
	}
	if reason := chain.Classify("mail.platform.test", roots, now, time.Hour); reason != "" {
		t.Fatalf("identity should classify clean, got %q", reason)
	}
	if gaps := certs.CoverageGaps(chain.Leaf, []string{"mail.platform.test", "autoconfig.platform.test"}); len(gaps) != 1 || gaps[0] != "autoconfig.platform.test" {
		t.Fatalf("coverage gaps = %v, want autoconfig.platform.test", gaps)
	}
	// Full declared coverage is the only trusted outcome of the probe.
	if got := verifyCertificateStatusRoots("mail.platform.test", raw, now, roots); !got.Trusted || got.Reason != "" {
		t.Fatalf("fully covered leaf must be trusted, got %+v", got)
	}
}

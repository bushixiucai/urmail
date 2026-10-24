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
	}{{"correct endpoint", "mail.platform.test", now, roots, true}, {"wrong tenant apex SAN", "example.test", now, roots, false}, {"untrusted bootstrap", "mail.platform.test", now, x509.NewCertPool(), false}, {"expired", "mail.platform.test", now.Add(13 * time.Hour), roots, false}, {"not yet valid", "mail.platform.test", now.Add(-2 * time.Hour), roots, false}} {
		t.Run(tc.name, func(t *testing.T) {
			got := verifyCertificateStatusRoots(tc.hostname, raw, tc.at, tc.trust)
			if got.Trusted != tc.want {
				t.Fatalf("%+v", got)
			}
		})
	}
}

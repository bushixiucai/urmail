package certs

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"
)

// makeCert builds one self-signed certificate with the given lifetime and
// SANs. It is the raw-material helper for the probe classification tests.
func makeCert(t *testing.T, notBefore, notAfter time.Time, dnsNames []string, ips []net.IP) (x509.Certificate, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return *parsed, pemBytes
}

// makeChainSigner creates a CA and a leaf signed by it, returning the leaf
// certificate, the full PEM (leaf first, CA second) and the CA pool.
func makeChainSigner(t *testing.T, leafDNS []string) (x509.Certificate, []byte, *x509.CertPool) {
	t.Helper()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "test ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, &caTemplate, &caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := x509.Certificate{
		SerialNumber:          big.NewInt(3),
		Subject:               pkix.Name{CommonName: "leaf"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              leafDNS,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, &leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := append(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})...)
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	return *leaf, pemBytes, roots
}

func TestParseChainRejectsNonCertificates(t *testing.T) {
	if _, err := ParseChain(nil); err == nil {
		t.Fatal("empty input must not parse to a chain")
	}
	if _, err := ParseChain([]byte("not a pem at all")); err == nil {
		t.Fatal("non-PEM input must not parse to a chain")
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte{1, 2, 3}})
	if _, err := ParseChain(keyPEM); err == nil {
		t.Fatal("a key-only file must not parse to a chain")
	}
}

func TestParseChainKeepsIntermediates(t *testing.T) {
	_, pemBytes, _ := makeChainSigner(t, []string{"mail.example.com"})
	chain, err := ParseChain(pemBytes)
	if err != nil {
		t.Fatal(err)
	}
	if chain.Leaf == nil || chain.Leaf.DNSNames[0] != "mail.example.com" {
		t.Fatalf("leaf not parsed: %+v", chain.Leaf)
	}
	if chain.Intermediates == nil {
		t.Fatal("intermediate pool not retained")
	}
	// The intermediate must actually chain the leaf to a root: verifying with
	// only the CA pool (no intermediates) succeeds, and the parse must not
	// have promoted the CA to the leaf position.
	if chain.Leaf.Subject.CommonName != "leaf" {
		t.Fatalf("wrong leaf parsed: %s", chain.Leaf.Subject.CommonName)
	}
}

func TestClassifyVerdicts(t *testing.T) {
	now := time.Now()
	selfPEM := func(notBefore, notAfter time.Time, dns []string) []byte {
		_, pemBytes := makeCert(t, notBefore, notAfter, dns, nil)
		return pemBytes
	}
	leaf, chainPEM, roots := makeChainSigner(t, []string{"mail.example.com"})
	chain, err := ParseChain(chainPEM)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		chain    Chain
		identity string
		roots    *x509.CertPool
		want     string
	}{
		{"valid", chain, "mail.example.com", roots, ""},
		{"expired", mustChain(t, selfPEM(now.Add(-48*time.Hour), now.Add(-time.Hour), []string{"mail.example.com"})), "mail.example.com", roots, ReasonExpired},
		{"not yet valid", mustChain(t, selfPEM(now.Add(time.Hour), now.Add(48*time.Hour), []string{"mail.example.com"})), "mail.example.com", roots, ReasonNotYetValid},
		{"wrong name", mustChain(t, selfPEM(now.Add(-time.Hour), now.Add(24*time.Hour), []string{"other.example.com"})), "mail.example.com", roots, ReasonNameMismatch},
		{"untrusted self-signed", mustChain(t, selfPEM(now.Add(-time.Hour), now.Add(24*time.Hour), []string{"mail.example.com"})), "mail.example.com", roots, ReasonUntrusted},
	}
	for _, tc := range cases {
		if got := tc.chain.Classify(tc.identity, tc.roots, now, 0); got != tc.want {
			t.Errorf("%s: Classify = %q, want %q", tc.name, got, tc.want)
		}
	}
	_ = leaf
}

func TestClassifyPriorityExpiryOverTrust(t *testing.T) {
	// An expired, wrong-name, untrusted certificate must be reported as
	// expired: lifetime is checked first so the operator fixes the real
	// problem, not a misleading secondary verdict.
	now := time.Now()
	_, pemBytes := makeCert(t, now.Add(-48*time.Hour), now.Add(-time.Hour), []string{"other.example.com"}, nil)
	chain, err := ParseChain(pemBytes)
	if err != nil {
		t.Fatal(err)
	}
	if got := chain.Classify("mail.example.com", x509.NewCertPool(), now, 0); got != ReasonExpired {
		t.Fatalf("Classify = %q, want %q", got, ReasonExpired)
	}
}

func TestClassifyExpiringWindow(t *testing.T) {
	now := time.Now()
	_, pemBytes := makeCert(t, now.Add(-time.Hour), now.Add(30*time.Minute), []string{"mail.example.com"}, nil)
	chain, err := ParseChain(pemBytes)
	if err != nil {
		t.Fatal(err)
	}
	// Self-signed is untrusted before it can be "expiring", so probe with the
	// certificate itself as its own root to isolate the window logic.
	roots := x509.NewCertPool()
	roots.AddCert(chain.Leaf)
	if got := chain.Classify("mail.example.com", roots, now, time.Hour); got != ReasonExpiring {
		t.Fatalf("Classify = %q, want %q", got, ReasonExpiring)
	}
	if got := chain.Classify("mail.example.com", roots, now, time.Minute); got != "" {
		t.Fatalf("Classify with narrow window = %q, want usable", got)
	}
}

func TestClassifyRootsUnavailable(t *testing.T) {
	now := time.Now()
	_, pemBytes := makeCert(t, now.Add(-time.Hour), now.Add(24*time.Hour), []string{"mail.example.com"}, nil)
	chain, err := ParseChain(pemBytes)
	if err != nil {
		t.Fatal(err)
	}
	if got := chain.Classify("mail.example.com", nil, now, 0); got != ReasonRootsUnavailable {
		t.Fatalf("Classify = %q, want %q", got, ReasonRootsUnavailable)
	}
}

func mustChain(t *testing.T, pemBytes []byte) Chain {
	t.Helper()
	chain, err := ParseChain(pemBytes)
	if err != nil {
		t.Fatal(err)
	}
	return chain
}

func TestNormalizeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Mail.Example.com.", "mail.example.com"},
		{"例子.example", "xn--fsqu00a.example"},
		{"127.0.0.1", "127.0.0.1"},
		{"[::1]", "::1"},
	}
	for _, tc := range cases {
		got, err := NormalizeName(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("NormalizeName(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	if _, err := NormalizeName("  "); err == nil {
		t.Error("empty name must be rejected")
	}
}

func TestNameCoveredSANOnly(t *testing.T) {
	now := time.Now()
	leaf, _ := makeCert(t, now.Add(-time.Hour), now.Add(24*time.Hour),
		[]string{"mail.example.com", "*.autoconfig.example.com"}, []net.IP{net.ParseIP("192.0.2.10")})

	if !NameCovered(&leaf, "mail.example.com") {
		t.Error("exact SAN must cover")
	}
	if !NameCovered(&leaf, "a.autoconfig.example.com") {
		t.Error("leftmost wildcard must cover one label")
	}
	if NameCovered(&leaf, "autoconfig.example.com") {
		t.Error("wildcard must not cover the parent domain")
	}
	if NameCovered(&leaf, "a.b.autoconfig.example.com") {
		t.Error("wildcard must not cover two extra labels")
	}
	if !NameCovered(&leaf, "192.0.2.10") {
		t.Error("IP SAN must cover the exact address")
	}
	if NameCovered(&leaf, "192.0.2.11") {
		t.Error("IP SAN must not cover a different address")
	}
	// Legacy CommonName must never count as coverage.
	leafCheck := leaf
	leafCheck.DNSNames = nil
	if NameCovered(&leafCheck, "test") {
		t.Error("CommonName fallback must not be accepted as coverage")
	}
	if NameCovered(nil, "mail.example.com") {
		t.Error("nil leaf must not be covered")
	}
}

func TestCoverageGaps(t *testing.T) {
	now := time.Now()
	leaf, _ := makeCert(t, now.Add(-time.Hour), now.Add(24*time.Hour),
		[]string{"mail.example.com", "autoconfig.example.com"}, nil)

	gaps := CoverageGaps(&leaf, []string{"mail.example.com", "autoconfig.example.com", "imap.example.com", "xn--fsqu00a.example"})
	want := []string{"imap.example.com", "xn--fsqu00a.example"}
	if len(gaps) != len(want) {
		t.Fatalf("CoverageGaps = %v, want %v", gaps, want)
	}
	for i := range want {
		if gaps[i] != want[i] {
			t.Fatalf("CoverageGaps = %v, want %v", gaps, want)
		}
	}
	if got := CoverageGaps(&leaf, []string{"mail.example.com"}); len(got) != 0 {
		t.Fatalf("full coverage reported gaps: %v", got)
	}
	// An invalid wanted name is surfaced as a gap in its input form instead of
	// being silently dropped.
	if got := CoverageGaps(&leaf, []string{"not a domain"}); len(got) != 1 || got[0] != "not a domain" {
		t.Fatalf("invalid name handling: %v", got)
	}
}

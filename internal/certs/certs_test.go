package certs_test

import (
	"crypto/x509"
	"encoding/pem"
	"path/filepath"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/certs"
)

func TestLoadOrCreateMasterKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "master.key")

	// First call generates
	key1, err := certs.LoadOrCreateMasterKey(keyPath)
	if err != nil {
		t.Fatalf("first LoadOrCreateMasterKey: %v", err)
	}
	if key1 == nil {
		t.Fatal("expected key, got nil")
	}

	// Second call loads the exact same key
	key2, err := certs.LoadOrCreateMasterKey(keyPath)
	if err != nil {
		t.Fatalf("second LoadOrCreateMasterKey: %v", err)
	}
	if !key1.Equal(key2) {
		t.Fatal("expected identical key on second load")
	}
}

func TestGenerateCSR(t *testing.T) {
	dir := t.TempDir()
	key, err := certs.LoadOrCreateMasterKey(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatalf("load key: %v", err)
	}

	csrPem, err := certs.GenerateCSR(key, "box.urmail.cn", []string{"mail.example.com", "autodiscover.example.com"})
	if err != nil {
		t.Fatalf("GenerateCSR: %v", err)
	}

	block, _ := pem.Decode(csrPem)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		t.Fatalf("invalid CSR PEM block")
	}

	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatalf("parse CSR: %v", err)
	}
	if csr.Subject.CommonName != "box.urmail.cn" {
		t.Errorf("expected CN box.urmail.cn, got %s", csr.Subject.CommonName)
	}
	if len(csr.DNSNames) != 3 {
		t.Errorf("expected 3 DNS names in SAN, got %d", len(csr.DNSNames))
	}
}

func TestGenerateSelfSignedCert(t *testing.T) {
	dir := t.TempDir()
	key, err := certs.LoadOrCreateMasterKey(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatalf("load key: %v", err)
	}

	certPem, err := certs.GenerateSelfSignedCert(key, "box.urmail.cn", []string{"mail.example.com"}, 365*24*time.Hour)
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert: %v", err)
	}

	block, _ := pem.Decode(certPem)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("invalid cert PEM block")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	if cert.Subject.CommonName != "box.urmail.cn" {
		t.Errorf("expected CN box.urmail.cn, got %s", cert.Subject.CommonName)
	}
}

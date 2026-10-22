package certs_test

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"sync"
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
func TestLoadOrCreateMasterKeyTightensMode(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "master.key")

	key, err := certs.LoadOrCreateMasterKey(keyPath)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Widen the mode, then reload: the loader must repair it to 0600.
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	key2, err := certs.LoadOrCreateMasterKey(keyPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !key.Equal(key2) {
		t.Fatal("key must be unchanged after mode tightening")
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("master key mode must be tightened to 0600, got %o", info.Mode().Perm())
	}
}

func TestLoadOrCreateMasterKeyConcurrent(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "master.key")

	const n = 16
	keys := make([]*rsa.PrivateKey, n)
	errs := make([]error, n)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			keys[i], errs[i] = certs.LoadOrCreateMasterKey(keyPath)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	for i := 1; i < n; i++ {
		if !keys[0].Equal(keys[i]) {
			t.Fatalf("goroutine %d produced a different key: concurrent creation must yield exactly one key", i)
		}
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("master key mode must be 0600, got %o", info.Mode().Perm())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected only the master key in the directory, got %d entries", len(entries))
	}
}

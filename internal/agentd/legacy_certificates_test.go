package agentd

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/certs"
)

func TestLegacyIssuedCertificateSurvivesRootStateUpgrade(t *testing.T) {
	m := testManager(t)
	ca, key, caPEM := certificateAuthority(t)
	caPath := filepath.Join(m.root, "ca.pem")
	if err := os.WriteFile(caPath, caPEM, 0600); err != nil {
		t.Fatal(err)
	}
	m.cfg.ACMERootCA = caPath
	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := certs.GenerateCSR(leafKey, m.hostname(), []string{m.hostname()})
	if err != nil {
		t.Fatal(err)
	}
	issued := signedCSR(t, csr, ca, key, caPEM, m.hostname(), time.Now().Add(60*24*time.Hour))
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leafKey)})
	oldCert := m.target(filepath.Join(m.cfg.StorageRoot, "ssl", m.hostname()+".cert"))
	oldKey := m.target(filepath.Join(m.cfg.StorageRoot, "ssl", m.hostname()+".key"))
	if err = os.MkdirAll(filepath.Dir(oldCert), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(oldCert, issued, 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(oldKey, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	// A legacy panel bootstrap must not win over the issued mail certificate.
	panelCert := m.target("/var/lib/urmail/certs/panel/fullchain.pem")
	panelKey := m.target("/var/lib/urmail/certs/panel/privkey.pem")
	if err = os.MkdirAll(filepath.Dir(panelCert), 0700); err != nil {
		t.Fatal(err)
	}
	bootstrap, err := certs.GenerateSelfSignedCert(leafKey, m.hostname(), []string{m.hostname()}, 365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(panelCert, bootstrap, 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(panelKey, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err = m.ensureBootstrap(ApplyParams{}); err != nil {
		t.Fatal(err)
	}
	for path := range m.certificatePaths(m.hostname()) {
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		expected := issued
		if filepath.Ext(path) == ".key" || filepath.Base(path) == "privkey.pem" {
			expected = keyPEM
		}
		if !bytes.Equal(got, expected) {
			t.Fatalf("legacy issued identity downgraded at %s", path)
		}
	}
}

func TestLegacyCertificateRejectsSymlinkAndInvalidIdentity(t *testing.T) {
	m := testManager(t)
	oldCert := m.target(filepath.Join(m.cfg.StorageRoot, "ssl", m.hostname()+".cert"))
	if err := os.MkdirAll(filepath.Dir(oldCert), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", oldCert); err != nil {
		t.Fatal(err)
	}
	if _, err := readLegacyCertificateFile(oldCert, 2<<20); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := m.ensureBootstrap(ApplyParams{}); err == nil {
		t.Fatal("damaged legacy identity silently replaced")
	}
}

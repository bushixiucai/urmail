package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

// provisionWireKey writes a real RSA key in the OpenDKIM-compatible layout
// (<dir>/<domain>/<selector>.private) and returns the public key.
func provisionWireKey(t *testing.T, dir, domain string) *rsa.PublicKey {
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
	if err = os.WriteFile(filepath.Join(domainDir, "mail.private"), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return &key.PublicKey
}

// txtForWire builds the mail._domainkey TXT record the verifier needs.
func txtForWire(pub *rsa.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		panic(err)
	}
	return "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(der)
}

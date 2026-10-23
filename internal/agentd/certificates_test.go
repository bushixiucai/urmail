package agentd

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/certs"
)

func certificateAuthority(t *testing.T) (*x509.Certificate, *rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Review Test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return ca, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
func signedCSR(t *testing.T, request []byte, ca *x509.Certificate, key *rsa.PrivateKey, caPEM []byte, domain string, expires time.Time) []byte {
	t.Helper()
	block, _ := pem.Decode(request)
	if block == nil {
		t.Fatal("missing CSR")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: domain}, DNSNames: []string{domain}, NotBefore: time.Now().Add(-time.Hour), NotAfter: expires, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, csr.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), caPEM...)
}
func TestCertificateCSRInstallRenewAndConsumerMapping(t *testing.T) {
	m := testManager(t)
	if err := m.ensureBootstrap(ApplyParams{}); err != nil {
		t.Fatal(err)
	}
	ca, key, caPEM := certificateAuthority(t)
	caPath := filepath.Join(m.root, "ca.pem")
	if err := os.WriteFile(caPath, caPEM, 0600); err != nil {
		t.Fatal(err)
	}
	m.cfg.ACMERootCA = caPath
	requests := 0
	m.run = func(_ context.Context, path string, args ...string) error {
		if path != "/usr/bin/certbot" {
			return nil
		}
		requests++
		values := map[string]string{}
		for i := 0; i+1 < len(args); i++ {
			values[args[i]] = args[i+1]
		}
		if values["--csr"] == "" || values["-w"] != m.acmeWebroot() {
			t.Fatal("CSR/webroot contract missing")
		}
		csr, err := os.ReadFile(values["--csr"])
		if err != nil {
			return err
		}
		issued := signedCSR(t, csr, ca, key, caPEM, m.hostname(), time.Now().Add(90*24*time.Hour))
		return os.WriteFile(values["--fullchain-path"], issued, 0600)
	}
	raw, _ := json.Marshal(CertificateParams{Domain: m.hostname()})
	if _, _, err := m.IssueCertificate(raw); err != nil {
		t.Fatal(err)
	}
	var fingerprint []byte
	for path := range m.certificatePaths(m.hostname()) {
		if filepath.Ext(path) == ".key" || filepath.Base(path) == "privkey.pem" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if fingerprint == nil {
			fingerprint = data
		} else if string(data) != string(fingerprint) {
			t.Fatal("panel/mail consumers differ")
		}
	}
	pair, err := tls.LoadX509KeyPair(certificateCertPath(m.certificatePaths(m.hostname())), certificateKeyPath(m.certificatePaths(m.hostname())))
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	if err := leaf.VerifyHostname(m.hostname()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.RenewCertificates(json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatal("valid CSR certificate unnecessarily renewed")
	}
	if _, _, err := m.IssueCertificate(json.RawMessage(`{"domain":"unregistered.example.com"}`)); err == nil {
		t.Fatal("unregistered certificate allowed")
	}
}
func TestCertificateRejectsBadIssuanceAndRestoresConsumers(t *testing.T) {
	for _, failure := range []string{"wrong-san", "wrong-key", "expired", "untrusted", "reload"} {
		t.Run(failure, func(t *testing.T) {
			m := testManager(t)
			if err := m.ensureBootstrap(ApplyParams{}); err != nil {
				t.Fatal(err)
			}
			path := certificateCertPath(m.certificatePaths(m.hostname()))
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			ca, key, caPEM := certificateAuthority(t)
			caPath := filepath.Join(m.root, "ca.pem")
			if err := os.WriteFile(caPath, caPEM, 0600); err != nil {
				t.Fatal(err)
			}
			if failure != "untrusted" {
				m.cfg.ACMERootCA = caPath
			}
			reloadFailed := false
			m.run = func(_ context.Context, path string, args ...string) error {
				if path == "/usr/bin/systemctl" && failure == "reload" && !reloadFailed {
					reloadFailed = true
					return errors.New("reload failed")
				}
				if path != "/usr/bin/certbot" {
					return nil
				}
				values := map[string]string{}
				for i := 0; i+1 < len(args); i++ {
					values[args[i]] = args[i+1]
				}
				csr, err := os.ReadFile(values["--csr"])
				if err != nil {
					return err
				}
				if failure == "wrong-key" {
					other, err := rsa.GenerateKey(rand.Reader, 2048)
					if err != nil {
						return err
					}
					der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: m.hostname()}, DNSNames: []string{m.hostname()}}, other)
					if err != nil {
						return err
					}
					csr = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
				}
				domain := m.hostname()
				expires := time.Now().Add(90 * 24 * time.Hour)
				if failure == "wrong-san" {
					domain = "wrong.example.com"
				}
				if failure == "expired" {
					expires = time.Now().Add(-time.Minute)
				}
				return os.WriteFile(values["--fullchain-path"], signedCSR(t, csr, ca, key, caPEM, domain, expires), 0600)
			}
			raw, _ := json.Marshal(CertificateParams{Domain: m.hostname()})
			if _, _, err := m.IssueCertificate(raw); err == nil {
				t.Fatal("bad issuance accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("failed issuance replaced previous certificate")
			}
		})
	}
}

func TestCertificateNetworkWaitDoesNotBlockConfiguration(t *testing.T) {
	m := testManager(t)
	if err := m.ensureBootstrap(ApplyParams{}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	m.run = func(_ context.Context, path string, args ...string) error {
		if path == "/usr/bin/certbot" {
			close(started)
			<-release
			return errors.New("test network failure")
		}
		return testCommand(m, path, args...)
	}
	go func() {
		defer close(done)
		raw, _ := json.Marshal(CertificateParams{Domain: m.hostname()})
		_, _, _ = m.IssueCertificate(raw)
	}()
	<-started
	applied := make(chan error, 1)
	go func() { _, _, err := m.Apply(emptyApply()); applied <- err }()
	select {
	case err := <-applied:
		if err != nil {
			close(release)
			<-done
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		<-done
		t.Fatal("network issuance blocked configuration apply")
	}
	close(release)
	<-done
}

func TestCertificateRenewsImportedECDSAIdentityWithoutChangingKey(t *testing.T) {
	m := testManager(t)
	ca, caKey, caPEM := certificateAuthority(t)
	caPath := filepath.Join(m.root, "ca.pem")
	if err := os.WriteFile(caPath, caPEM, 0600); err != nil {
		t.Fatal(err)
	}
	m.cfg.ACMERootCA = caPath
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := certs.GenerateCSR(key, m.hostname(), []string{m.hostname()})
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	old := signedCSR(t, csr, ca, caKey, caPEM, m.hostname(), time.Now().Add(time.Hour))
	if err = m.installCertificateFiles(m.hostname(), old, keyPEM); err != nil {
		t.Fatal(err)
	}
	requests := 0
	m.run = func(_ context.Context, path string, args ...string) error {
		if path != "/usr/bin/certbot" {
			return nil
		}
		requests++
		values := map[string]string{}
		for i := 0; i+1 < len(args); i++ {
			values[args[i]] = args[i+1]
		}
		request, readErr := os.ReadFile(values["--csr"])
		if readErr != nil {
			return readErr
		}
		block, _ := pem.Decode(request)
		parsed, parseErr := x509.ParseCertificateRequest(block.Bytes)
		if parseErr != nil {
			return parseErr
		}
		if parsed.SignatureAlgorithm != x509.ECDSAWithSHA256 {
			t.Fatalf("wrong CSR algorithm %v", parsed.SignatureAlgorithm)
		}
		public, ok := parsed.PublicKey.(*ecdsa.PublicKey)
		if !ok || !public.Equal(&key.PublicKey) {
			t.Fatal("CSR changed legacy ECDSA identity")
		}
		issued := signedCSR(t, request, ca, caKey, caPEM, m.hostname(), time.Now().Add(90*24*time.Hour))
		return os.WriteFile(values["--fullchain-path"], issued, 0600)
	}
	if _, _, err = m.RenewCertificates(json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("renew requests %d", requests)
	}
	actual, err := os.ReadFile(certificateKeyPath(m.certificatePaths(m.hostname())))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, keyPEM) {
		t.Fatal("renewal changed imported ECDSA key")
	}
}

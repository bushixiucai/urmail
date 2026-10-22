package certs

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// KeySize is the default RSA key size for the platform master key.
const KeySize = 2048

// loadMasterKey reads and parses the key file. It tightens the file mode to
// 0600 when the key exists but is readable by group/other.
func loadMasterKey(keyPath string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	if info, statErr := os.Stat(keyPath); statErr == nil {
		if info.Mode().Perm() != 0o600 {
			// Repair an over-permissive key file (M2 review P2).
			if err := os.Chmod(keyPath, 0o600); err != nil {
				return nil, fmt.Errorf("tighten master key mode: %w", err)
			}
		}
	}
	block, _ := pem.Decode(data)
	if block == nil {
		// Wrapping errIncompleteKey lets a racing creator retry until the
		// winner finishes writing, while a permanently corrupt key file
		// still fails loudly in LoadOrCreateMasterKey.
		return nil, fmt.Errorf("decode master key: %w", errIncompleteKey)
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

// LoadOrCreateMasterKey reads the RSA master private key from keyPath, or
// generates and saves one (mode 0600) if absent.
//
// The create path is concurrency-safe: the destination file is claimed with
// O_CREAT|O_EXCL, the key material is written to a temporary file, fsynced,
// and atomically renamed into place, followed by a directory fsync. A racing
// process that loses the O_EXCL race re-reads the winner's key instead of
// overwriting it (M2 review P2).
func LoadOrCreateMasterKey(keyPath string) (*rsa.PrivateKey, error) {
	if keyPath == "" {
		return nil, errors.New("master key path is empty")
	}

	key, err := loadMasterKey(keyPath)
	if err == nil {
		return key, nil
	}
	// fs.ErrNotExist: create. errIncompleteKey: the file exists but is not a
	// parseable key yet — fall through so the O_EXCL claim detects the
	// racing creator and waits instead of failing (see waitForMasterKey).
	if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, errIncompleteKey) {
		return nil, fmt.Errorf("load master key: %w", err)
	}

	// Claim the destination atomically.
	claim, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			// Another process is creating the key right now: load theirs.
			return waitForMasterKey(keyPath)
		}
		return nil, fmt.Errorf("claim master key file: %w", err)
	}
	_ = claim.Close()

	key, genErr := rsa.GenerateKey(rand.Reader, KeySize)
	if genErr != nil {
		_ = os.Remove(keyPath)
		return nil, fmt.Errorf("generate master key: %w", genErr)
	}

	pemBlock := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}

	if err := writeKeyAtomic(keyPath, pem.EncodeToMemory(pemBlock)); err != nil {
		_ = os.Remove(keyPath)
		return nil, err
	}
	return key, nil
}

// writeKeyAtomic writes key material to a sibling temp file, fsyncs it,
// renames it over keyPath, and fsyncs the containing directory.
func writeKeyAtomic(keyPath string, data []byte) error {
	dir := filepath.Dir(keyPath)

	tmp, err := os.CreateTemp(dir, ".master-key-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp key file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName) // no-op after successful rename
	}()

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp key file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp key file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync temp key file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp key file: %w", err)
	}

	if err := os.Rename(tmpName, keyPath); err != nil {
		return fmt.Errorf("rename master key into place: %w", err)
	}

	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// waitForMasterKey polls for a concurrently created key file.
func waitForMasterKey(keyPath string) (*rsa.PrivateKey, error) {
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		key, err := loadMasterKey(keyPath)
		if err == nil {
			return key, nil
		}
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, errIncompleteKey) {
			return nil, fmt.Errorf("load master key: %w", err)
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	return nil, fmt.Errorf("master key not created by concurrent writer: %w", lastErr)
}

// errIncompleteKey marks a key file that exists but is not parseable yet
// (partially written by another process).
var errIncompleteKey = errors.New("master key file incomplete")

// LoadMasterKey reads the master key without changing its permissions. The
// installer deliberately makes it group-readable by urmaild, so the
// tightening behavior of LoadOrCreateMasterKey is not appropriate here.
func LoadMasterKey(keyPath string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("master key is not PEM")
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

// DomainCertDir is where urmaild keeps self-signed/ACME certificates for
// tenant domains. It is under the service's ReadWritePaths.
const DomainCertDir = "/var/lib/urmail/certs/domains"

// EnsureDomainCertificate creates a self-signed certificate/key for fqdn if
// neither exists. It never overwrites an ACME certificate.
func EnsureDomainCertificate(masterKeyPath, fqdn string) (string, string, error) {
	if err := os.MkdirAll(DomainCertDir, 0o750); err != nil {
		return "", "", fmt.Errorf("create domain cert dir: %w", err)
	}
	certPath := filepath.Join(DomainCertDir, fqdn+".cert")
	keyPath := filepath.Join(DomainCertDir, fqdn+".key")
	if _, err := os.Stat(certPath); err == nil {
		if _, err := os.Stat(keyPath); err == nil {
			return certPath, keyPath, nil
		}
	}
	if masterKeyPath == "" {
		return "", "", errors.New("master key path is empty")
	}
	key, err := LoadMasterKey(masterKeyPath)
	if err != nil {
		return "", "", fmt.Errorf("load master key: %w", err)
	}
	certPEM, err := GenerateSelfSignedCert(key, fqdn, []string{fqdn}, 365*24*time.Hour)
	if err != nil {
		return "", "", err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := writeCertFile(certPath, certPEM, 0o644); err != nil {
		return "", "", err
	}
	if err := writeCertFile(keyPath, keyPEM, 0o600); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

func writeCertFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".urmail-cert-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// GenerateCSR creates a PKCS#10 Certificate Signing Request in PEM format for the given domains.
func GenerateCSR(key *rsa.PrivateKey, primaryDomain string, sans []string) ([]byte, error) {
	sanSet := make(map[string]bool)
	var dnsNames []string
	for _, d := range append([]string{primaryDomain}, sans...) {
		if d != "" && !sanSet[d] {
			sanSet[d] = true
			dnsNames = append(dnsNames, d)
		}
	}

	subj := pkix.Name{
		CommonName: primaryDomain,
	}

	template := x509.CertificateRequest{
		Subject:            subj,
		DNSNames:           dnsNames,
		SignatureAlgorithm: x509.SHA256WithRSA,
	}

	csrBytes, err := x509.CreateCertificateRequest(rand.Reader, &template, key)
	if err != nil {
		return nil, fmt.Errorf("create csr: %w", err)
	}

	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csrBytes,
	}), nil
}

// GenerateSelfSignedCert produces a self-signed fallback certificate for the given domains using the master key.
func GenerateSelfSignedCert(key *rsa.PrivateKey, primaryDomain string, sans []string, validFor time.Duration) ([]byte, error) {
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, err
	}

	sanSet := make(map[string]bool)
	var dnsNames []string
	var ipAddrs []net.IP
	for _, d := range append([]string{primaryDomain}, sans...) {
		if d != "" && !sanSet[d] {
			sanSet[d] = true
			if ip := net.ParseIP(d); ip != nil {
				ipAddrs = append(ipAddrs, ip)
			} else {
				dnsNames = append(dnsNames, d)
			}
		}
	}

	notBefore := time.Now().Add(-1 * time.Hour)
	notAfter := notBefore.Add(validFor)

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"urMail.cn Self-Signed Fallback"},
			CommonName:   primaryDomain,
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ipAddrs,
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create self-signed cert: %w", err)
	}

	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certBytes,
	}), nil
}

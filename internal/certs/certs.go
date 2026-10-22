package certs

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// KeySize is the default RSA key size for the platform master key.
const KeySize = 2048

// LoadOrCreateMasterKey reads the RSA master private key from keyPath, or generates and saves one (mode 0600) if absent.
func LoadOrCreateMasterKey(keyPath string) (*rsa.PrivateKey, error) {
	if data, err := os.ReadFile(keyPath); err == nil {
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, errors.New("failed to decode master key PEM")
		}
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	}

	// Generate new key
	key, err := rsa.GenerateKey(rand.Reader, KeySize)
	if err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return nil, err
	}

	pemBlock := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}

	if err := os.WriteFile(keyPath, pem.EncodeToMemory(pemBlock), 0600); err != nil {
		return nil, fmt.Errorf("write master key: %w", err)
	}
	return key, nil
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

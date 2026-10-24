package agentd

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"time"
)

// OpCertificateStatus is a fixed-path read-only trusted-chain probe.
const OpCertificateStatus = "cert.status"

// CertificateStatus reports only public certificate facts, never key bytes.
type CertificateStatus struct {
	Domain    string    `json:"domain"`
	Trusted   bool      `json:"trusted"`
	ExpiresAt time.Time `json:"expires_at"`
	Reason    string    `json:"reason,omitempty"`
}

// CertificateStatus validates CA chain, exact DNS SAN and current lifetime.
// Bootstrap self-signed certificates can serve configuration challenges but
// never satisfy this business readiness gate.
func (m *ApplyManager) CertificateStatus(raw json.RawMessage) (string, string, error) {
	var p CertificateParams
	if err := strictParams(raw, &p); err != nil {
		return "", "", err
	}
	m.mu.Lock()
	allowed := m.certificateAllowed(p.Domain)
	m.mu.Unlock()
	if !allowed {
		return "", "", fmt.Errorf("%w: certificate identity is not registered", ErrRejected)
	}
	result := CertificateStatus{Domain: p.Domain, Reason: "CERTIFICATE_UNAVAILABLE"}
	path := certificateCertPath(m.certificatePaths(p.Domain))
	info, err := os.Lstat(path)
	if err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 && info.Size() <= 1<<20 {
		raw, e := os.ReadFile(path) // #nosec G304 -- registered identity maps exclusively to a regular file in the root-owned agent certificate directory.
		if e == nil {
			result = verifyCertificateStatus(p.Domain, raw, time.Now())
		}
	}
	out, err := json.Marshal(result)
	return string(out), "", err
}
func verifyCertificateStatus(domain string, raw []byte, now time.Time) CertificateStatus {
	roots, err := x509.SystemCertPool()
	if err != nil {
		return CertificateStatus{Domain: domain, Reason: "CERTIFICATE_ROOTS_UNAVAILABLE"}
	}
	return verifyCertificateStatusRoots(domain, raw, now, roots)
}
func verifyCertificateStatusRoots(domain string, raw []byte, now time.Time, roots *x509.CertPool) CertificateStatus {
	out := CertificateStatus{Domain: domain, Reason: "CERTIFICATE_INVALID"}
	var leaf *x509.Certificate
	intermediates := x509.NewCertPool()
	for {
		block, rest := pem.Decode(raw)
		if block == nil {
			break
		}
		raw = rest
		if block.Type != "CERTIFICATE" {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return out
		}
		if leaf == nil {
			leaf = certificate
		} else {
			intermediates.AddCert(certificate)
		}
	}
	if leaf == nil {
		return out
	}
	out.ExpiresAt = leaf.NotAfter
	_, err := leaf.Verify(x509.VerifyOptions{DNSName: domain, Roots: roots, Intermediates: intermediates, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	if err != nil {
		out.Reason = "CERTIFICATE_CHAIN_OR_SAN_INVALID"
		return out
	}
	if leaf.NotAfter.Before(now.Add(time.Hour)) {
		out.Reason = "CERTIFICATE_EXPIRING"
		return out
	}
	out.Trusted = true
	out.Reason = ""
	return out
}

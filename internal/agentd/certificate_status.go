package agentd

import (
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/bushixiucai/urmail/internal/certs"
)

// OpCertificateStatus is a fixed-path read-only trusted-chain probe.
const OpCertificateStatus = "cert.status"

// CertificateStatus reports only public certificate facts, never key bytes.
type CertificateStatus struct {
	Domain    string    `json:"domain"`
	Trusted   bool      `json:"trusted"`
	ExpiresAt time.Time `json:"expires_at"`
	Reason    string    `json:"reason,omitempty"`
	// MissingSANs lists the declared endpoint identities the leaf does not
	// cover (MAIL-006 SAN completeness). Empty unless Reason is
	// CERTIFICATE_SAN_INCOMPLETE.
	MissingSANs []string `json:"missing_sans,omitempty"`
}

// certificateEndpointIdentities returns every endpoint served with this
// certificate. Today each registered identity (mail hostname, tenant domain,
// brand host) terminates its own vhost with its own certificate, so the
// declared set is the identity itself; autoconfig/DAV/EAS are paths under the
// same vhost and inherit the same name. Keep this the single place that grows
// if additional per-domain endpoint hostnames are introduced.
func certificateEndpointIdentities(domain string) []string {
	return []string{domain}
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
	result := CertificateStatus{Domain: p.Domain, Reason: certs.ReasonUnavailable}
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
		return CertificateStatus{Domain: domain, Reason: certs.ReasonRootsUnavailable}
	}
	return verifyCertificateStatusRoots(domain, raw, now, roots)
}

// verifyCertificateStatusRoots classifies one consumer chain for one identity.
// Reasons are the stable certs.* verdict codes: lifetime (not-yet-valid /
// expired), then name, then trust, then the renewal window. A name-mismatched
// certificate is never collapsed into "untrusted" and an unusable certificate
// is never reported as trusted with a cosmetic reason.
func verifyCertificateStatusRoots(domain string, raw []byte, now time.Time, roots *x509.CertPool) CertificateStatus {
	chain, err := certs.ParseChain(raw)
	if err != nil {
		return CertificateStatus{Domain: domain, Reason: certs.ReasonInvalid}
	}
	reason := chain.Classify(domain, roots, now, time.Hour)
	out := CertificateStatus{Domain: domain, Reason: reason}
	if chain.Leaf != nil {
		out.ExpiresAt = chain.Leaf.NotAfter
	}
	if reason != "" {
		return out
	}
	// The identity itself is served; the SAN gate must still hold for every
	// declared endpoint name so a partially-scoped certificate cannot pass.
	if gaps := certs.CoverageGaps(chain.Leaf, certificateEndpointIdentities(domain)); len(gaps) > 0 {
		out.Reason = certs.ReasonSANIncomplete
		out.MissingSANs = gaps
		return out
	}
	out.Trusted = true
	return out
}

package agentd

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/certs"
	"github.com/bushixiucai/urmail/internal/mail/nginx"
	"github.com/bushixiucai/urmail/internal/mail/validation"
	"github.com/bushixiucai/urmail/internal/store"
)

// OpCertificateIssue issues for a registered domain using an agent-owned key.
const OpCertificateIssue = "cert.issue"

// OpCertificateRenew reissues expiring CSR certificates.
const OpCertificateRenew = "cert.renew"

// CertificateParams selects an authorized identity, never filesystem paths.
type CertificateParams struct {
	Domain string `json:"domain"`
}

func (m *ApplyManager) hostname() string {
	if m.cfg.MyHostname != "" {
		return m.cfg.MyHostname
	}
	return "box." + m.cfg.PrimaryDomain
}
func (m *ApplyManager) certificateRoots() []string {
	return []string{filepath.Join(m.root, "certs")}
}
func (m *ApplyManager) certificatePaths(domain string) map[string]os.FileMode {
	if domain == m.hostname() {
		return map[string]os.FileMode{filepath.Join(m.root, "certs/panel/fullchain.pem"): 0644, filepath.Join(m.root, "certs/panel/privkey.pem"): 0600, filepath.Join(m.root, "certs/mail", domain+".cert"): 0644, filepath.Join(m.root, "certs/mail", domain+".key"): 0600}
	}
	return map[string]os.FileMode{filepath.Join(m.root, "certs/domains", domain+".cert"): 0644, filepath.Join(m.root, "certs/domains", domain+".key"): 0600}
}
func certificateKeyPath(paths map[string]os.FileMode) string {
	names := make([]string, 0, len(paths))
	for path := range paths {
		names = append(names, path)
	}
	sort.Strings(names)
	for _, path := range names {
		if strings.HasSuffix(path, "privkey.pem") {
			return path
		}
	}
	for _, path := range names {
		if strings.HasSuffix(path, ".key") {
			return path
		}
	}
	return ""
}
func certificateCertPath(paths map[string]os.FileMode) string {
	names := make([]string, 0, len(paths))
	for path := range paths {
		names = append(names, path)
	}
	sort.Strings(names)
	for _, path := range names {
		if strings.HasSuffix(path, "fullchain.pem") {
			return path
		}
	}
	for _, path := range names {
		if strings.HasSuffix(path, ".cert") {
			return path
		}
	}
	return ""
}

func (m *ApplyManager) ensureBootstrap(p ApplyParams) error {
	if len(p.Postfix.Domains) > 10000 {
		return fmt.Errorf("too many domains")
	}
	deadline := time.Now().Add(30 * time.Second)
	domains := []string{m.hostname()}
	for _, d := range p.Postfix.Domains {
		if m.customWebEnabled(d.FQDN) {
			domains = append(domains, d.FQDN)
		}
	}
	for _, domain := range domains {
		if !m.certificateProvisioningAllowed(domain) {
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("certificate bootstrap time budget exhausted")
		}
		if !validation.IsValidHostname(domain) || domain != strings.TrimSpace(domain) {
			return fmt.Errorf("%w: invalid certificate identity", ErrRejected)
		}
		paths := m.certificatePaths(domain)
		certPath, keyPath := certificateCertPath(paths), certificateKeyPath(paths)
		if _, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
			// Already bootstrapped. Do not rewrite: hostname paths include both
			// panel and mail files, and rewriting panel material over mail would
			// clobber a Let's Encrypt leaf (and fail under chattr +i).
			continue
		}
		// Never overwrite an existing (possibly damaged) issued certificate with a fallback.
		if _, err := os.Lstat(certPath); err == nil {
			return fmt.Errorf("existing certificate cannot be loaded")
		}
		if _, err := os.Lstat(keyPath); err == nil {
			return fmt.Errorf("existing certificate key cannot be loaded")
		}
		imported, err := m.importLegacyCertificate(domain)
		if err != nil {
			return err
		}
		if imported {
			continue
		}
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return err
		}
		certificate, err := certs.GenerateSelfSignedCert(key, domain, []string{domain}, 365*24*time.Hour)
		if err != nil {
			return err
		}
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
		if err := m.installCertificateFiles(domain, certificate, keyPEM); err != nil {
			return err
		}
	}
	return nil
}

// IssueCertificate validates the identity and installs a trusted certificate.
func (m *ApplyManager) IssueCertificate(raw json.RawMessage) (string, string, error) {
	var p CertificateParams
	if err := strictParams(raw, &p); err != nil {
		return "", "", err
	}
	m.mu.Lock()
	allowed := m.certificateAllowed(p.Domain)
	m.mu.Unlock()
	if !allowed || !validation.IsValidHostname(p.Domain) || !m.certificateProvisioningAllowed(p.Domain) {
		return "", "", fmt.Errorf("%w: certificate domain is not registered", ErrRejected)
	}
	// Brand hosts (OPS-101) are issued only after ownership verification;
	// they are intentionally absent from the configured domain list above.
	if !m.certificateAllowedBrandHost(p.Domain) {
		return "", "", fmt.Errorf("%w: brand host is not verified", ErrRejected)
	}
	m.certMu.Lock()
	defer m.certMu.Unlock()
	if err := m.issueCertificate(p.Domain); err != nil {
		m.recordCertificateError("certificate issuance failed")
		return "", "", fmt.Errorf("%w: certificate issuance failed", ErrFailed)
	}
	m.recordCertificateError("")
	return p.Domain, "", nil
}

func (m *ApplyManager) certificateAllowed(domain string) bool {
	if domain == m.hostname() {
		return true
	}
	for _, d := range m.domains {
		if d == domain {
			return true
		}
	}
	return false
}

// brandHostCertEligible reports whether the hostname is a brand host whose
// ownership has been verified (OPS-101). Pending-verify or failed brand
// hosts are never issued material, and the store query fails closed.
func (m *ApplyManager) brandHostCertEligible(domain string) bool {
	if m.pool == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	eligible, err := store.New(m.pool.Pool).PlatformBrandHostCertEligible(ctx, domain)
	return err == nil && eligible
}

// certificateAllowedBrandHost accepts a domain when it is either a configured
// platform domain or a verified brand host.
func (m *ApplyManager) certificateAllowedBrandHost(domain string) bool {
	m.mu.Lock()
	allowed := m.certificateAllowed(domain)
	m.mu.Unlock()
	if allowed {
		return true
	}
	return m.brandHostCertEligible(domain)
}
func (m *ApplyManager) recordCertificateError(message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.CertificateError = message
	_ = m.saveJSON("manifest.json", applyManifest{Status: m.status, Paths: m.paths, Domains: m.domains})
}

// certRenewBackoff bounds how often a failing ACME renewal is retried: the
// first failure delays the next automatic attempt by 5 minutes, doubling up to
// a hard cap of 8 hours. The delay only gates automatic renewals — a failure
// never removes the previous certificate or blocks explicit operator action
// for longer than one backoff window.
const (
	certRenewBackoffBase = 5 * time.Minute
	certRenewBackoffMax  = 8 * time.Hour
)

func certRenewDelay(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	delay := certRenewBackoffBase
	for i := 1; i < failures; i++ {
		delay *= 2
		if delay >= certRenewBackoffMax {
			return certRenewBackoffMax
		}
	}
	if delay > certRenewBackoffMax {
		return certRenewBackoffMax
	}
	return delay
}

// RenewCertificates handles CSR certificates without a certbot renewal lineage. Reissue explicitly using
// the locally held private key when less than thirty days remain.
//
// Only hostname (mail) certificate failures flip the global certificate_error /
// ErrFailed status. Tenant domain issuance failures keep any existing self-signed
// fallback and do not mark the box as failed — hostname LE health is independent.
//
// MAIL-006: consecutive ACME failures back off exponentially (bounded by
// certRenewBackoffMax) so a stuck provider is not hammered on every config
// apply. During the backoff window the call is a no-op that keeps the last
// failure status; the installed certificates and service config are untouched.
func (m *ApplyManager) RenewCertificates(raw json.RawMessage) (string, string, error) {
	var empty struct{}
	if err := strictParams(raw, &empty); err != nil {
		return "", "", err
	}
	m.certMu.Lock()
	defer m.certMu.Unlock()
	if time.Now().Before(m.certRenewNextTry) {
		return "certificate renewal deferred by backoff", "", nil
	}
	hostname := m.hostname()
	m.mu.Lock()
	domains := append([]string{hostname}, m.domains...)
	m.mu.Unlock()
	var hostnameFailed bool
	deadline := time.Now().Add(3 * time.Minute)
	for _, domain := range domains {
		if !m.certificateProvisioningAllowed(domain) {
			continue
		}
		if time.Now().After(deadline) {
			if domain == hostname {
				hostnameFailed = true
			}
			break
		}
		path := certificateCertPath(m.certificatePaths(domain))
		raw, err := os.ReadFile(path) // #nosec G304 -- canonical agent-owned consumer path.
		if err == nil {
			block, _ := pem.Decode(raw)
			if block != nil {
				certificate, e := x509.ParseCertificate(block.Bytes)
				if e == nil && certificate.Issuer.String() != certificate.Subject.String() && time.Until(certificate.NotAfter) > 30*24*time.Hour {
					continue
				}
			}
		}
		budget := min(time.Until(deadline), 2*time.Minute)
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		if err := m.issueCertificateContext(ctx, domain); err != nil {
			if domain == hostname {
				hostnameFailed = true
			}
			// Domain issuance failed: leave existing fallback cert in place.
		}
		cancel()
	}
	if hostnameFailed {
		m.certRenewFails++
		m.certRenewNextTry = time.Now().Add(certRenewDelay(m.certRenewFails))
		m.recordCertificateError("certificate renewal failed")
		return "", "", fmt.Errorf("%w: certificate renewal failed", ErrFailed)
	}
	// Hostname is current or was successfully reissued — clear sticky error
	// and reset the failure backoff.
	m.certRenewFails = 0
	m.certRenewNextTry = time.Time{}
	m.recordCertificateError("")
	return "certificates current", "", nil
}

func (m *ApplyManager) issueCertificate(domain string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return m.issueCertificateContext(ctx, domain)
}

func (m *ApplyManager) issueCertificateContext(ctx context.Context, domain string) error {
	paths := m.certificatePaths(domain)
	keyPath := certificateKeyPath(paths)
	keyPEM, err := os.ReadFile(keyPath) // #nosec G304 -- canonical agent-owned private key.
	if err != nil {
		return err
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return fmt.Errorf("invalid private key")
	}
	var key crypto.Signer
	if parsed, e := x509.ParsePKCS1PrivateKey(block.Bytes); e == nil {
		key = parsed
	} else if parsed, e := x509.ParsePKCS8PrivateKey(block.Bytes); e == nil {
		key, _ = parsed.(crypto.Signer)
	} else if parsed, e := x509.ParseECPrivateKey(block.Bytes); e == nil {
		key = parsed
	}
	if key == nil {
		return fmt.Errorf("unsupported private key")
	}
	csr, err := certs.GenerateCSR(key, domain, []string{domain})
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp(m.root, "csr-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	csrPath := filepath.Join(dir, "request.pem")
	if err := os.WriteFile(csrPath, csr, 0600); err != nil { // #nosec G703 -- private root-owned temporary directory and fixed filename.
		return err
	}
	webroot := m.acmeWebroot()
	if err := os.MkdirAll(webroot, 0755); err != nil {
		return err
	}
	fullchain := filepath.Join(dir, "fullchain.pem")
	args := []string{"certonly", "--csr", csrPath, "--webroot", "-w", webroot, "--preferred-challenges", "http", "--non-interactive", "--agree-tos", "--email", "hostmaster@" + m.cfg.PrimaryDomain, "--cert-path", filepath.Join(dir, "cert.pem"), "--chain-path", filepath.Join(dir, "chain.pem"), "--fullchain-path", fullchain}
	if m.cfg.ACMEServer != "" {
		args = append(args, "--server", m.cfg.ACMEServer)
	}
	if err := m.run(ctx, "/usr/bin/certbot", args...); err != nil {
		return err
	}
	issued, err := os.ReadFile(fullchain) // #nosec G304 -- fixed filename in an agent-owned temporary directory.
	if err != nil {
		return err
	}
	if err := validateCertificate(issued, keyPEM, domain, m.cfg.ACMERootCA, time.Now()); err != nil {
		return err
	}
	// Network issuance runs without the configuration mutex. Only the local
	// consumer switch is serialized with config.apply.
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.certificateAllowed(domain) {
		return fmt.Errorf("certificate domain was removed during issuance")
	}
	currentKey, err := os.ReadFile(keyPath) // #nosec G304 -- recheck the canonical root-owned key after issuance.
	if err != nil || !bytes.Equal(currentKey, keyPEM) {
		return fmt.Errorf("certificate private key changed during issuance")
	}
	installCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	backups := map[string]savedFile{}
	for path := range paths {
		f, err := saveFile(path)
		if err != nil {
			return err
		}
		backups[path] = f
	}
	rollback := func() error {
		for path, f := range backups {
			if f.Exists {
				if _, err := writeFileAtomic(path, f.Content, os.FileMode(f.Mode), m.certificateRoots()); err != nil {
					return err
				}
			} else {
				_ = os.Remove(path)
			}
		}
		return nil
	}
	if err := m.installCertificateFiles(domain, issued, keyPEM); err != nil {
		_ = rollback()
		return err
	}
	checks := [][]string{{"/usr/sbin/nginx", "-t"}}
	units := []string{"nginx"}
	if domain == m.hostname() {
		checks = append(checks, []string{"/usr/bin/doveconf", "-n"}, []string{"/usr/sbin/postfix", "check"})
		units = append(units, "dovecot", "postfix")
	}
	reload := func() error {
		for _, c := range checks {
			if err := m.run(installCtx, c[0], c[1:]...); err != nil {
				return err
			}
		}
		for _, unit := range units {
			if err := m.run(installCtx, "/usr/bin/systemctl", "reload", unit); err != nil {
				return err
			}
		}
		return nil
	}
	if err := reload(); err != nil {
		if restoreErr := rollback(); restoreErr != nil {
			m.status.Degraded = true
		}
		if restoreErr := reload(); restoreErr != nil {
			m.status.Degraded = true
		}
		return err
	}
	return nil
}

func (m *ApplyManager) installCertificateFiles(domain string, certificate, key []byte) error {
	for path, mode := range m.certificatePaths(domain) {
		data := certificate
		if strings.HasSuffix(path, ".key") || strings.HasSuffix(path, "privkey.pem") {
			data = key
		}
		if _, err := writeFileAtomic(path, data, mode, m.certificateRoots()); err != nil {
			return err
		}
	}
	return nil
}

func validateCertificate(certificate, key []byte, domain, rootCA string, now time.Time) error {
	pair, err := tls.X509KeyPair(certificate, key)
	if err != nil {
		return fmt.Errorf("certificate key mismatch")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	if err := leaf.VerifyHostname(domain); err != nil {
		return err
	}
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return fmt.Errorf("certificate validity failed")
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return err
	}
	if rootCA != "" {
		raw, err := os.ReadFile(rootCA) // #nosec G304 -- root CA path is provided exclusively by the root environment.
		if err != nil {
			return err
		}
		if !roots.AppendCertsFromPEM(raw) {
			return fmt.Errorf("invalid ACME root CA")
		}
	}
	intermediates := x509.NewCertPool()
	for _, raw := range pair.Certificate[1:] {
		c, err := x509.ParseCertificate(raw)
		if err != nil {
			return err
		}
		intermediates.AddCert(c)
	}
	_, err = leaf.Verify(x509.VerifyOptions{DNSName: domain, Roots: roots, Intermediates: intermediates, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}

func (m *ApplyManager) acmeWebroot() string {
	if m.webroot != "" {
		return m.webroot
	}
	return nginx.ACMEWebroot
}

func (m *ApplyManager) customWebEnabled(domain string) bool {
	if domain == m.hostname() || m.pool == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	enabled, err := store.New(m.pool.Pool).PlatformCustomWebEnabled(ctx, domain)
	return err == nil && enabled
}
func (m *ApplyManager) certificateProvisioningAllowed(domain string) bool {
	if domain == m.hostname() || m.pool == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	allowed, err := store.New(m.pool.Pool).PlatformProvisioningCertificateAllowed(ctx, domain)
	return err == nil && allowed
}

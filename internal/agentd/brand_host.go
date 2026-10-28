package agentd

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"time"

	"github.com/bushixiucai/urmail/internal/certs"
	"github.com/bushixiucai/urmail/internal/mail/nginx"
	"github.com/bushixiucai/urmail/internal/mail/validation"
	"github.com/bushixiucai/urmail/internal/reconcile"
	"github.com/bushixiucai/urmail/internal/store"
)

// OpBrandHostActivate names the verified brand's certificate/config lifecycle.
const OpBrandHostActivate = "brand.host.activate"

// BrandHostParams fences a hostname against rebinding or ownership replacement.
type BrandHostParams struct {
	ActorID    int64     `json:"actor_id,omitempty"`
	Hostname   string    `json:"hostname"`
	BrandID    int64     `json:"brand_id"`
	VerifiedAt time.Time `json:"verified_at"`
}

func (m *ApplyManager) brandNginxDomains() ([]nginx.DomainConfig, error) {
	if m.pool == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := store.New(m.pool).PlatformListBrandHosts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]nginx.DomainConfig, 0, len(rows))
	for _, row := range rows {
		if !m.brandHostCertEligible(row.Hostname) {
			continue
		}
		if !validation.IsValidHostname(row.Hostname) {
			return nil, fmt.Errorf("invalid brand hostname")
		}
		paths := m.certificatePaths(row.Hostname)
		out = append(out, nginx.DomainConfig{FQDN: row.Hostname, TLSCertFile: certificateCertPath(paths), TLSKeyFile: certificateKeyPath(paths), ACMEOnly: row.Status == "verified", EnableWebmail: m.cfg.RoundcubeEnabled})
	}
	return out, nil
}

func (m *ApplyManager) bootstrapBrandCertificate(host string) error {
	if !m.brandHostCertEligible(host) {
		return fmt.Errorf("%w: brand host is not verified", ErrRejected)
	}
	paths := m.certificatePaths(host)
	cert, key := certificateCertPath(paths), certificateKeyPath(paths)
	if _, err := os.Stat(key); err == nil {
		return nil
	}
	if _, err := os.Stat(cert); err == nil {
		return fmt.Errorf("brand certificate private key missing")
	}
	signer, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	fallback, err := certs.GenerateSelfSignedCert(signer, host, []string{host}, 24*time.Hour)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(signer)})
	return m.installCertificateFiles(host, fallback, keyPEM)
}

// ActivateBrandHost issues with live authorization, applies the exact database
// snapshot, and publishes active only after the real config apply succeeds.
func (m *ApplyManager) ActivateBrandHost(raw json.RawMessage) (string, string, error) {
	if m.cfg == nil || m.cfg.WorkersHeld || m.cfg.Quarantined || m.cfg.DevMode {
		return "", "", fmt.Errorf("%w: brand lifecycle is held or externally managed", ErrRejected)
	}
	m.brandMu.Lock()
	defer m.brandMu.Unlock()
	var p BrandHostParams
	if err := strictParams(raw, &p); err != nil {
		return "", "", err
	}
	if !validation.IsValidHostname(p.Hostname) || p.BrandID <= 0 || p.ActorID <= 0 || p.VerifiedAt.IsZero() || m.pool == nil {
		return "", "", fmt.Errorf("%w: invalid brand identity", ErrRejected)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	q := store.New(m.pool)
	reserved, reservedErr := q.PlatformBrandHostReserved(ctx, p.Hostname)
	if reservedErr != nil || reserved || p.Hostname == m.hostname() {
		return "", "", fmt.Errorf("%w: hostname is reserved", ErrRejected)
	}
	if p.ActorID > 0 {
		allowed, e := q.CheckPlatformAdmin(ctx, p.ActorID)
		if e != nil || !allowed {
			return "", "", fmt.Errorf("%w: platform authorization revoked", ErrRejected)
		}
	}
	original, err := q.PlatformGetBrandHostLifecycle(ctx, p.Hostname)
	if err != nil || original.BrandID != p.BrandID || !original.VerifiedAt.Time.Equal(p.VerifiedAt) || !m.brandHostCertEligible(p.Hostname) {
		return "", "", fmt.Errorf("%w: brand authorization changed", ErrRejected)
	}
	if m.cfg.PreserveExternalWeb {
		domains, e := m.brandNginxDomains()
		if e != nil {
			return "", "", e
		}
		if e = m.applyHybridBrandFiles(domains); e != nil {
			return "", "", e
		}
	}
	if err = m.bootstrapBrandCertificate(p.Hostname); err != nil {
		return "", "", err
	}
	request, _ := json.Marshal(CertificateParams{Domain: p.Hostname})
	if _, _, err = m.IssueCertificate(request); err != nil {
		return "", "", err
	}
	allowed, e := q.CheckPlatformAdmin(ctx, p.ActorID)
	if e != nil || !allowed {
		return "", "", fmt.Errorf("%w: platform authorization revoked during issuance", ErrRejected)
	}
	transition := store.PlatformBrandHostCertificateTransitionParams{ActorID: p.ActorID, Hostname: p.Hostname, BrandID: p.BrandID, VerifiedAt: original.VerifiedAt, ExpectedStatus: original.Status, NextStatus: "cert-issued"}
	n, err := q.PlatformBrandHostCertificateTransition(ctx, transition)
	if err != nil || n != 1 {
		return "", "", fmt.Errorf("%w: brand authorization changed", ErrRejected)
	}
	snapshot, err := reconcile.AssembleConfigContext(ctx, m.cfg, &reconcile.VersionedStore{StoreQuerier: store.New(m.pool), DB: m.pool.Pool}, m.cfg.PublicIPv4, m.cfg.Nameservers)
	if err == nil {
		payload, _ := json.Marshal(ApplyParams{Postfix: snapshot.PostfixSnap, DNS: snapshot.NSDSnap, DKIM: snapshot.DKIM, SpamPolicies: snapshot.DovecotNode.SpamPolicies})
		_, _, err = m.Apply(payload)
	}
	if err != nil {
		return "", "", fmt.Errorf("%w: brand configuration apply failed; certificate retained", ErrFailed)
	}
	transition.ExpectedStatus = "cert-issued"
	transition.NextStatus = "active"
	n, err = q.PlatformBrandHostCertificateTransition(ctx, transition)
	if err != nil || n != 1 {
		return "", "", fmt.Errorf("%w: brand authorization changed", ErrRejected)
	}
	return `{"status":"active"}`, "", nil
}

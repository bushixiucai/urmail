package agentd

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// importLegacyCertificate reads only known consumers from the published layout.
// Existing certificates are parsed as untrusted data and never executed or
// supplied to a subprocess. A damaged existing pair blocks fallback issuance.
func (m *ApplyManager) importLegacyCertificate(domain string) (bool, error) {
	type pair struct{ cert, key string }
	candidates := []pair{}
	if domain == m.hostname() {
		candidates = append(candidates, pair{"/var/lib/urmail/certs/panel/fullchain.pem", "/var/lib/urmail/certs/panel/privkey.pem"})
	}
	base := filepath.Join(m.cfg.StorageRoot, "ssl", domain)
	candidates = append(candidates, pair{base + ".cert", base + ".key"})
	var invalid bool
	var bootstrapCert, bootstrapKey []byte
	for _, candidate := range candidates {
		certificate, certErr := readLegacyCertificateFile(m.target(candidate.cert), 2<<20)
		key, keyErr := readLegacyCertificateFile(m.target(candidate.key), 64<<10)
		if os.IsNotExist(certErr) && os.IsNotExist(keyErr) {
			continue
		}
		if certErr != nil || keyErr != nil {
			invalid = true
			continue
		}
		parsed, err := tls.X509KeyPair(certificate, key)
		if err != nil || len(parsed.Certificate) == 0 {
			invalid = true
			continue
		}
		leaf, err := x509.ParseCertificate(parsed.Certificate[0])
		if err != nil {
			invalid = true
			continue
		}
		now := time.Now()
		if leaf.VerifyHostname(domain) != nil || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
			invalid = true
			continue
		}
		// A previously issued certificate must retain its verified public chain.
		// A valid legacy self-signed bootstrap can also be carried forward, but
		// never replaces a successfully imported issued pair.
		if leaf.Issuer.String() == leaf.Subject.String() {
			bootstrapCert, bootstrapKey = certificate, key
			continue
		}
		if validateCertificate(certificate, key, domain, m.cfg.ACMERootCA, now) != nil {
			invalid = true
			continue
		}
		if err = m.installCertificateFiles(domain, certificate, key); err != nil {
			return false, err
		}
		return true, nil
	}
	if invalid {
		return false, fmt.Errorf("legacy certificate pair requires repair for %s", domain)
	}
	if bootstrapCert != nil {
		return true, m.installCertificateFiles(domain, bootstrapCert, bootstrapKey)
	}
	return false, nil
}

func readLegacyCertificateFile(path string, limit int64) ([]byte, error) {
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator)), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("legacy certificate symlink rejected")
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("invalid legacy certificate file")
	}
	file, err := os.Open(path) // #nosec G304 -- fixed legacy consumer selected from root configuration, checked against symlinks and bounded below.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("legacy certificate changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("legacy certificate exceeds limit")
	}
	return data, nil
}

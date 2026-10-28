package agentd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bushixiucai/urmail/internal/mail/nginx"
	"github.com/bushixiucai/urmail/internal/mail/postfix"
	"github.com/bushixiucai/urmail/internal/mail/validation"
	"github.com/bushixiucai/urmail/internal/store"
)

// transportHosts derives certificate identities from current database-owned
// publications, never from a caller-supplied arbitrary hostname or file path.
func (m *ApplyManager) transportHosts(domains []postfix.Domain) ([]string, error) {
	if m.pool == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows, err := store.New(m.pool).PlatformListTransportSecurity(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) > 10000 {
		return nil, fmt.Errorf("transport policy count exceeds limit")
	}
	registered := make(map[string]bool, len(domains))
	for _, domain := range domains {
		registered[domain.FQDN] = domain.Status == "active"
	}
	var out []string
	for _, row := range rows {
		if registered[row.Fqdn] {
			host := "mta-sts." + row.Fqdn
			if !validation.IsValidHostname(host) {
				return nil, fmt.Errorf("invalid transport policy host")
			}
			out = append(out, host)
		}
	}
	return out, nil
}
func (m *ApplyManager) transportCertificateAllowed(host string) bool {
	if m.pool == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	allowed, err := store.New(m.pool).PlatformTransportCertificateAllowed(ctx, host)
	return err == nil && allowed
}
func (m *ApplyManager) transportNginxDomains() []nginx.DomainConfig {
	out := make([]nginx.DomainConfig, 0, len(m.transportDomains))
	for _, host := range m.transportDomains {
		cert := filepath.Join(m.root, "certs/domains", host+".cert")
		key := filepath.Join(m.root, "certs/domains", host+".key")
		if _, err := os.Stat(cert); err != nil {
			continue
		}
		if _, err := os.Stat(key); err != nil {
			continue
		}
		out = append(out, nginx.DomainConfig{FQDN: host, TLSCertFile: cert, TLSKeyFile: key, TransportOnly: true})
	}
	return out
}

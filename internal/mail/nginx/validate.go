package nginx

import (
	"fmt"
	"strings"

	"github.com/bushixiucai/urmail/internal/mail/validation"
)

// Validate checks the node configuration and snapshot.
func Validate(node NodeConfig, snap Snapshot) error {
	if !validation.IsValidHostname(node.PrimaryDomain) {
		return fmt.Errorf("%w: invalid primary domain %q", ErrInvalidConfig, node.PrimaryDomain)
	}
	if strings.TrimSpace(node.StorageRoot) == "" {
		return fmt.Errorf("%w: storage root cannot be empty", ErrInvalidConfig)
	}
	if strings.TrimSpace(node.BackendHTTP) == "" {
		return fmt.Errorf("%w: backend HTTP address cannot be empty", ErrInvalidConfig)
	}

	seen := make(map[string]bool, len(snap.Domains))
	for _, d := range snap.Domains {
		fqdn := strings.ToLower(strings.TrimSpace(d.FQDN))
		if !validation.IsValidHostname(fqdn) {
			return fmt.Errorf("%w: invalid domain FQDN %q", ErrInvalidDomain, d.FQDN)
		}
		if seen[fqdn] {
			return fmt.Errorf("%w: duplicate domain %q", ErrInvalidDomain, d.FQDN)
		}
		seen[fqdn] = true
		if strings.TrimSpace(d.TLSCertFile) == "" {
			return fmt.Errorf("%w: domain %s missing TLS cert file", ErrInvalidDomain, d.FQDN)
		}
		if strings.TrimSpace(d.TLSKeyFile) == "" {
			return fmt.Errorf("%w: domain %s missing TLS key file", ErrInvalidDomain, d.FQDN)
		}
	}
	return nil
}
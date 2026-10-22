package nginx

import (
	"fmt"
	"regexp"
	"strings"
)

var hostnameRegex = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}$`)

func isValidHostname(h string) bool {
	if len(h) == 0 || len(h) > 255 {
		return false
	}
	return hostnameRegex.MatchString(h)
}

// Validate checks the node configuration and snapshot.
func Validate(node NodeConfig, snap Snapshot) error {
	if !isValidHostname(node.PrimaryDomain) {
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
		if !isValidHostname(fqdn) {
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

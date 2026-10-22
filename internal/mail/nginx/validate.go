package nginx

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/bushixiucai/urmail/internal/mail/validation"
)

// backendPattern restricts proxy backend targets to a scheme plus
// host[:port]. Anything else could smuggle spaces, additional directives or
// URL parts into the nginx config (M2 review P1-4).
var backendPattern = regexp.MustCompile(`^https?://[A-Za-z0-9.:-]+$`)

// bodySizePattern restricts client_max_body_size to nginx size syntax.
var bodySizePattern = regexp.MustCompile(`^[0-9]+[kKmM]?$`)

func validateBackend(kind, value string) error {
	if !backendPattern.MatchString(value) {
		return fmt.Errorf("%w: %s %q must match http(s)://host[:port]", ErrInvalidConfig, kind, value)
	}
	return nil
}

// Validate checks the node configuration and snapshot.
func Validate(node NodeConfig, snap Snapshot) error {
	if !validation.IsValidHostname(node.PrimaryDomain) {
		return fmt.Errorf("%w: invalid primary domain %q", ErrInvalidConfig, node.PrimaryDomain)
	}
	if err := validation.ValidateAbsolutePath(node.StorageRoot); err != nil {
		return fmt.Errorf("%w: storage root: %v", ErrInvalidConfig, err)
	}
	if err := validateBackend("backend HTTP", node.BackendHTTP); err != nil {
		return err
	}
	if node.WebmailBackend != "" {
		if err := validateBackend("webmail backend", node.WebmailBackend); err != nil {
			return err
		}
	}
	if node.ZPushBackend != "" {
		if err := validateBackend("z-push backend", node.ZPushBackend); err != nil {
			return err
		}
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
		if err := validation.ValidateAbsolutePath(d.TLSCertFile); err != nil {
			return fmt.Errorf("%w: domain %s TLS cert file: %v", ErrInvalidDomain, d.FQDN, err)
		}
		if err := validation.ValidateAbsolutePath(d.TLSKeyFile); err != nil {
			return fmt.Errorf("%w: domain %s TLS key file: %v", ErrInvalidDomain, d.FQDN, err)
		}
		size := d.ClientMaxBodySize
		if size == "" {
			size = "128M"
		}
		if !bodySizePattern.MatchString(size) {
			return fmt.Errorf("%w: domain %s client_max_body_size %q is invalid", ErrInvalidDomain, d.FQDN, d.ClientMaxBodySize)
		}
	}
	return nil
}

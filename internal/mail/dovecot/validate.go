package dovecot

import (
	"fmt"

	"github.com/bushixiucai/urmail/internal/dovecotauth"

	"github.com/bushixiucai/urmail/internal/mail/validation"
)

// Validate checks the node configuration.
func Validate(node NodeConfig) error {
	if !validation.IsValidHostname(node.Hostname) {
		return fmt.Errorf("%w: invalid hostname %q", ErrInvalidConfig, node.Hostname)
	}
	if !validation.IsValidHostname(node.PrimaryDomain) {
		return fmt.Errorf("%w: invalid primary domain %q", ErrInvalidConfig, node.PrimaryDomain)
	}
	if err := validation.ValidateAbsolutePath(node.StorageRoot); err != nil {
		return fmt.Errorf("%w: storage root: %w", ErrInvalidConfig, err)
	}
	if err := validation.ValidateAbsolutePath(node.TLSCertFile); err != nil {
		return fmt.Errorf("%w: TLS certificate file: %w", ErrInvalidConfig, err)
	}
	if err := validation.ValidateAbsolutePath(node.TLSKeyFile); err != nil {
		return fmt.Errorf("%w: TLS key file: %w", ErrInvalidConfig, err)
	}
	// §5: dovecot authenticates against the read-only SQLite snapshot, so the
	// legacy PostgreSQL DSN is no longer part of the rendered contract; the
	// snapshot path is validated instead.
	authDB := node.AuthDBPath
	if authDB == "" {
		authDB = dovecotauth.DefaultPath
	}
	if err := validation.ValidateAbsolutePath(authDB); err != nil {
		return fmt.Errorf("%w: auth snapshot path: %w", ErrInvalidConfig, err)
	}
	return validateSpamPolicies(node.SpamPolicies)
}

package dovecot

import (
	"fmt"
	"strings"

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
	if strings.TrimSpace(node.StorageRoot) == "" {
		return fmt.Errorf("%w: storage root cannot be empty", ErrInvalidConfig)
	}
	if strings.TrimSpace(node.DatabaseURL) == "" {
		return fmt.Errorf("%w: database URL cannot be empty", ErrInvalidConfig)
	}
	return nil
}
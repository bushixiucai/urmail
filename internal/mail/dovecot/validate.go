package dovecot

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

// Validate checks the node configuration.
func Validate(node NodeConfig) error {
	if !isValidHostname(node.Hostname) {
		return fmt.Errorf("%w: invalid hostname %q", ErrInvalidConfig, node.Hostname)
	}
	if !isValidHostname(node.PrimaryDomain) {
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

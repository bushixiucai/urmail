package dovecot

import "errors"

var (
	// ErrInvalidConfig is returned when NodeConfig validation fails.
	ErrInvalidConfig = errors.New("invalid dovecot node configuration")
)

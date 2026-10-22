package nginx

import "errors"

var (
	// ErrInvalidConfig is returned when NodeConfig fails validation.
	ErrInvalidConfig = errors.New("invalid nginx node configuration")

	// ErrInvalidDomain is returned when a domain configuration is malformed.
	ErrInvalidDomain = errors.New("invalid nginx domain configuration")
)

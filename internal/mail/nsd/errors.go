package nsd

import "errors"

var (
	// ErrInvalidConfig is returned when NodeConfig is malformed.
	ErrInvalidConfig = errors.New("invalid nsd node configuration")

	// ErrInvalidZone is returned when a zone domain or mode is invalid.
	ErrInvalidZone = errors.New("invalid dns zone")

	// ErrInvalidRecord is returned when a DNS record is malformed.
	ErrInvalidRecord = errors.New("invalid dns record")
)

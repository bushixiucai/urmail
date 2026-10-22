package postfix

import "errors"

var (
	// ErrInvalidConfig is returned when NodeConfig fails validation.
	ErrInvalidConfig = errors.New("invalid node configuration")

	// ErrInvalidDomain is returned when a domain FQDN is invalid or duplicated.
	ErrInvalidDomain = errors.New("invalid domain")

	// ErrInvalidAddress is returned when a mailbox address is malformed.
	ErrInvalidAddress = errors.New("invalid mailbox address")

	// ErrInvalidTarget is returned when an alias target is invalid.
	ErrInvalidTarget = errors.New("invalid alias target")

	// ErrInvalidAlias is returned when alias syntax is invalid (e.g. non-empty LocalPart for catchall).
	ErrInvalidAlias = errors.New("invalid alias")

	// ErrAliasCycle is returned when an alias routing cycle is detected.
	ErrAliasCycle = errors.New("alias cycle detected")

	// ErrAliasConflict is returned when there are multiple aliases for the same (domain, local_part)
	// or an alias conflicts with an existing mailbox address.
	ErrAliasConflict = errors.New("alias conflict")

	// ErrInvalidStatus is returned when a non-active entity is present in the snapshot.
	ErrInvalidStatus = errors.New("invalid status: entity is not active")
)

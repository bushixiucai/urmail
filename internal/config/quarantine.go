package config

import (
	"errors"
	"os"
)

// RestoreQuarantineMarker is outside every managed restore archive root. Its
// presence overrides restored environment files and prevents worker replay.
const RestoreQuarantineMarker = "/var/lib/urmail-restore-quarantine"

func restoreQuarantinePresent(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, os.ErrNotExist)
}

// RestoreQuarantined rechecks the immutable marker for long-lived privileged processes.
func RestoreQuarantined() bool { return restoreQuarantinePresent(RestoreQuarantineMarker) }

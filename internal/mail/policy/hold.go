package policy

import (
	"os"
	"path/filepath"
)

// HoldReason is the SMTP deferral returned while the upgrade/restore hold
// marker exists. Postfix turns DEFER_IF_PERMIT into a 4xx reply carrying this
// text, so a sender keeps the message in its own queue and retries after the
// hold is released; nothing is accepted or delivered into the target.
const HoldReason = "4.7.1 Upgrade in progress; mail deferred until cutover completes"

// HoldGate reports whether the maintenance hold marker currently exists.
// The gate is stateless: the marker is re-read on every policy request so a
// hold created or released by urmailctl takes effect without restarting the
// policy service. An empty path disables the gate.
type HoldGate struct {
	Path string
}

// Held reports whether a regular hold marker is present. A missing path, a
// directory, or an unreadable location does not hold mail; the maintenance
// fence itself fails closed on its own side.
func (g HoldGate) Held() bool {
	path := g.Path
	if path == "" {
		return false
	}
	if !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}

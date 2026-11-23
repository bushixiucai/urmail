package policy

import (
	"errors"
	"os"
	"path/filepath"
)

// HoldReason is the SMTP deferral returned while the upgrade/restore hold
// marker exists. Postfix turns DEFER_IF_PERMIT into a 4xx reply carrying this
// text, so a sender keeps the message in its own queue and retries after the
// hold is released; nothing is accepted or delivered into the target.
const HoldReason = "4.7.1 Upgrade in progress; mail deferred until cutover completes"

// HoldState is the three-way result of inspecting the maintenance marker.
// Only an absence proven inside a trusted, inspectable directory opens mail.
type HoldState int

const (
	// HoldOpen means the trusted directory was inspected and the marker is
	// definitively absent.
	HoldOpen HoldState = iota
	// HoldActive means a regular hold marker is present.
	HoldActive
	// HoldUnknown means the gate could not prove absence: an abnormal marker
	// type, a symlink, an unsearchable or untrusted parent, or an access error.
	// Callers must treat it exactly like an active hold.
	HoldUnknown
)

// HoldGate reports whether the maintenance hold marker currently exists.
// The gate is stateless: the marker is re-read on every policy request so a
// hold created or released by urmailctl takes effect without restarting the
// policy service. An empty path disables the gate explicitly.
type HoldGate struct {
	Path string
}

// State inspects the configured marker without following links. A missing
// marker only opens the gate when its parent is a real, inspectable directory
// that is re-checked after the marker lookup. Every other outcome, including a
// directory/symlink at the marker path, a permission error, an unsearchable
// parent or a replaced parent, is HoldUnknown and must defer mail.
func (g HoldGate) State() HoldState {
	if g.Path == "" {
		return HoldOpen
	}
	if !filepath.IsAbs(g.Path) {
		return HoldUnknown
	}
	parent := filepath.Dir(g.Path)
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() {
		return HoldUnknown
	}
	info, err := os.Lstat(g.Path)
	switch {
	case err == nil:
		if info.Mode().IsRegular() {
			return HoldActive
		}
		return HoldUnknown
	case errors.Is(err, os.ErrNotExist):
		// The absence only counts when the same trusted parent is still
		// inspectable; a vanished or replaced parent proves nothing.
		again, againErr := os.Lstat(parent)
		if againErr != nil || !again.IsDir() {
			return HoldUnknown
		}
		return HoldOpen
	default:
		return HoldUnknown
	}
}

// Held reports whether mail must be deferred: an active marker or any state
// that could not prove a clean absence.
func (g HoldGate) Held() bool { return g.State() != HoldOpen }
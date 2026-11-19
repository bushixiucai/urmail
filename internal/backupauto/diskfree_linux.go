//go:build linux

package backupauto

import "syscall"

// dirFreeBytes reports the space available to an unprivileged writer in the
// directory holding path (A1, v0.6.25): the cipher spool doubles one
// artifact's occupancy of that directory for the duration of one upload.
func dirFreeBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	if st.Bsize <= 0 {
		// Exotic or unreadable filesystem geometry: treat as unknown so
		// callers skip the advisory precheck instead of blocking on it.
		return 0, nil
	}
	return st.Bavail * uint64(st.Bsize), nil
}

//go:build linux || darwin

package agentd

import (
	"os"
	"syscall"
)

func fileOwner(info os.FileInfo) (int, int) {
	s, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, -1
	}
	return int(s.Uid), int(s.Gid)
}

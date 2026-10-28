//go:build linux || darwin

package agentd

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
)

func fileOwner(info os.FileInfo) (int, int) {
	s, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, -1
	}
	return int(s.Uid), int(s.Gid)
}

func chownNSDDirectory(path string) error {
	account, err := user.Lookup("nsd")
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return err
	}
	return os.Chown(path, uid, gid)
}

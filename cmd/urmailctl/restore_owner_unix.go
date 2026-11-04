//go:build darwin || linux

package main

import (
	"archive/tar"
	"errors"
	"os"
	"syscall"
)

func preserveFileOwnership(file *os.File, info os.FileInfo) error {
	uid, gid := -1, -1
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		uid, gid = int(stat.Uid), int(stat.Gid)
	} else if hdr, ok := info.Sys().(*tar.Header); ok {
		uid, gid = hdr.Uid, hdr.Gid
	}
	if uid < 0 || gid < 0 {
		return nil
	}
	if err := file.Chown(uid, gid); err != nil {
		if os.Geteuid() != 0 && (os.IsPermission(err) || errors.Is(err, syscall.EPERM)) {
			return nil
		}
		return err
	}
	return nil
}

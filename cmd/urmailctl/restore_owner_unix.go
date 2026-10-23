//go:build darwin || linux

package main

import (
	"os"
	"syscall"
)

func preserveFileOwnership(file *os.File, info os.FileInfo) error {
	stat := info.Sys().(*syscall.Stat_t)
	return file.Chown(int(stat.Uid), int(stat.Gid))
}

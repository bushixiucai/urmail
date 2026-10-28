//go:build linux

package sievegate

import (
	"fmt"
	"golang.org/x/sys/unix"
	"net"
)

func authorize(conn *net.UnixConn, uid uint32) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var credential *unix.Ucred
	var credErr error
	if err = raw.Control(func(fd uintptr) {
		credential, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if credErr != nil {
		return credErr
	}
	if credential == nil || credential.Uid != uid {
		return fmt.Errorf("sieve gate peer rejected")
	}
	return nil
}

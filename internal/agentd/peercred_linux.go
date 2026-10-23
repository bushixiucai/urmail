//go:build linux

package agentd

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// authorizePeer enforces the peer authentication policy on Linux.
//
// SO_PEERCRED is filled in by the kernel when the connection is accepted, so
// the reported UID cannot be spoofed by the client. The check runs before any
// request parsing.
func authorizePeer(conn *net.UnixConn, policy *peerPolicy) error {
	uid, err := peerUID(conn)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRejected, err)
	}
	if !policy.allows(uid) {
		return fmt.Errorf("%w: peer uid %d is not allowed to use the agent", ErrRejected, uid)
	}
	return nil
}

// peerUID returns the effective UID of the process at the other end of conn.
func peerUID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("get raw connection: %w", err)
	}

	var (
		cred    *unix.Ucred
		credErr error
	)
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, fmt.Errorf("read peer credentials: %w", err)
	}
	if credErr != nil {
		return 0, fmt.Errorf("read peer credentials: %w", credErr)
	}
	return cred.Uid, nil
}

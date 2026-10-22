//go:build linux

package agentd

import (
	"net"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPeerUIDReportsCurrentProcess(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	file := os.NewFile(uintptr(fds[0]), "agent-peer-test")
	conn, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		_ = unix.Close(fds[1])
		t.Fatalf("wrap socket: %v", err)
	}
	defer unix.Close(fds[1])
	defer conn.Close()

	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		t.Fatalf("expected a unix connection, got %T", conn)
	}

	uid, err := peerUID(unixConn)
	if err != nil {
		t.Fatalf("read peer uid: %v", err)
	}
	if uid != uint32(os.Geteuid()) {
		t.Fatalf("expected peer uid %d, got %d", os.Geteuid(), uid)
	}
}

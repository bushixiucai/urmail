//go:build linux

package sievegate

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestKernelPeerAndLiveDecision(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "gate.sock")
		ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		uid := uint32(os.Getuid())
		if rejected {
			uid++
		}
		called := make(chan string, 1)
		done := make(chan error, 1)
		go func() {
			done <- Serve(ctx, ln, uid, func(_ context.Context, address string) (bool, error) { called <- address; return true, nil }, nil)
		}()
		err = checkPath(ctx, path, "alice@example.test")
		cancel()
		if serveErr := <-done; serveErr != nil {
			t.Fatal(serveErr)
		}
		if rejected {
			if err == nil {
				t.Fatal("wrong UID accepted")
			}
			select {
			case <-called:
				t.Fatal("rejected peer reached DB")
			default:
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			if <-called != "alice@example.test" {
				t.Fatal("principal changed")
			}
		}
	}
}

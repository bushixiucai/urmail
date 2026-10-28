package sievegate

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHelperRejectsCallerPrincipal(t *testing.T) {
	for _, tc := range []struct {
		args []string
		user string
	}{{[]string{"other@example.test"}, "alice@example.test"}, {nil, ""}, {nil, " alice@example.test"}, {nil, "alice@example.test\n"}} {
		if err := RunHelper(tc.args, func(string) string { return tc.user }); err == nil {
			t.Fatal("untrusted principal accepted")
		}
	}
}
func TestCheckRequiresExplicitBoundedAllow(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		allowed       bool
	}{{"allow", `{"allowed":true}`, true}, {"deny", `{"allowed":false}`, false}, {"broken", `not-json`, false}, {"empty", `{}`, false}} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "sievegate-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			path := filepath.Join(dir, "gate.sock")
			listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, acceptErr := listener.Accept()
				if acceptErr != nil {
					return
				}
				defer conn.Close()
				var req request
				if json.NewDecoder(conn).Decode(&req) != nil || req.Address != "alice@example.test" {
					return
				}
				_, _ = conn.Write([]byte(tc.payload))
			}()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			err = checkPath(ctx, path, "alice@example.test")
			if (err == nil) != tc.allowed {
				t.Fatalf("result %v", err)
			}
			<-done
		})
	}
	if err := checkPath(t.Context(), filepath.Join(t.TempDir(), "missing"), "alice@example.test"); err == nil {
		t.Fatal("missing service allowed outbound")
	}
	if err := checkPath(t.Context(), "missing", "bad"); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}

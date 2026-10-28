package sievegate

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsolatedHelperUsesInterpreterUserAndRequiresLiveAllow(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required by the Linux package")
	}
	for _, allowed := range []bool{true, false} {
		directory, err := os.MkdirTemp("/tmp", "sg-python-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(directory) })
		path := filepath.Join(directory, "socket")
		listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
		if err != nil {
			t.Fatal(err)
		}
		script := filepath.Join(directory, "helper.py")
		if err = os.WriteFile(script, []byte(strings.ReplaceAll(HelperScript, SocketPath, path)), 0600); err != nil {
			t.Fatal(err)
		}
		principal := make(chan string, 1)
		go func() {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			var req request
			if json.NewDecoder(conn).Decode(&req) != nil {
				return
			}
			principal <- req.Address
			_ = json.NewEncoder(conn).Encode(response{Allowed: allowed})
		}()
		command := exec.CommandContext(t.Context(), python, "-I", script)
		command.Env = []string{"USER=alice@example.test", "RECIPIENT=other@example.test", "HOME=" + directory, "PYTHONPATH=" + directory}
		output, runErr := command.CombinedOutput()
		_ = listener.Close()
		if (runErr == nil) != allowed {
			t.Fatalf("live allow=%v: %v %s", allowed, runErr, output)
		}
		if <-principal != "alice@example.test" {
			t.Fatal("helper trusted the envelope recipient instead of the interpreter")
		}
		command = exec.CommandContext(t.Context(), python, "-I", script, "other@example.test")
		command.Env = []string{"USER=alice@example.test"}
		if command.Run() == nil {
			t.Fatal("helper accepted a caller principal argument")
		}
	}
}

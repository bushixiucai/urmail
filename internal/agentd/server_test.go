package agentd_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/agent"
	"github.com/bushixiucai/urmail/internal/agentd"
)

// testSocketPath returns a short unix socket path.
//
// The path has to stay short: macOS limits unix socket paths to 104 bytes,
// and the per-test temporary directory can be longer than that.
func testSocketPath(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "ur")
	if err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return filepath.Join(dir, "a.sock")
}

func TestServerRoundTrip(t *testing.T) {
	socket := testSocketPath(t)

	registry := agentd.NewRegistry()
	registry.Register("echo", func(params json.RawMessage) (string, string, error) {
		return string(params), "", nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := agentd.NewServer(socket, registry, discardLogger())
	if err := server.Listen(ctx); err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = server.Serve() }()

	client := agent.NewClient(socket)

	response, err := client.Do(ctx, "echo", map[string]string{"hello": "world"})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if !response.OK {
		t.Fatalf("expected success, got %+v", response.Error)
	}
	if response.Stdout != `{"hello":"world"}` {
		t.Fatalf("unexpected payload %q", response.Stdout)
	}
}

func TestServerRejectsUnknownOperation(t *testing.T) {
	socket := testSocketPath(t)

	registry := agentd.NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := agentd.NewServer(socket, registry, discardLogger())
	if err := server.Listen(ctx); err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = server.Serve() }()

	client := agent.NewClient(socket)
	response, err := client.Do(ctx, "shell.exec", map[string]string{"cmd": "id"})
	if err == nil {
		t.Fatal("rejected operation must return an error")
	}
	if response.OK {
		t.Fatal("an unknown operation must not succeed")
	}
	if response.Error == nil || response.Error.Code != agentd.CodeUnknownOp {
		t.Fatalf("unexpected error %+v", response.Error)
	}
}

func TestSocketPermissionsRestrictAccess(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the permission check is meaningless as root")
	}

	socket := testSocketPath(t)

	registry := agentd.NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := agentd.NewServer(socket, registry, discardLogger())
	if err := server.Listen(ctx); err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = server.Serve() }()

	info, err := os.Stat(socket)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0o660 {
		t.Fatalf("the socket must be mode 0660, got %04o", perm)
	}
}

func TestClientReportsAnUnreachableSocket(t *testing.T) {
	client := agent.NewClient(filepath.Join(t.TempDir(), "missing.sock"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := client.Do(ctx, "echo", nil); err == nil {
		t.Fatal("connecting to a missing socket must fail")
	}
}

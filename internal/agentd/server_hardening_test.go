package agentd_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bushixiucai/urmail/internal/agent"
	"github.com/bushixiucai/urmail/internal/agentd"
)

func TestServerRecoversFromOperationPanic(t *testing.T) {
	socket := testSocketPath(t)

	registry := agentd.NewRegistry()
	registry.Register("panic", func(json.RawMessage) (string, string, error) {
		panic("operation exploded")
	})
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

	response, err := client.Do(ctx, "panic", nil)
	if err == nil || response == nil {
		t.Fatalf("operation failure must return response and error: %v", err)
	}
	if response.OK {
		t.Fatal("a panicking operation must not report success")
	}
	if response.Error == nil || response.Error.Code != agentd.CodeFailed {
		t.Fatalf("expected %s, got %+v", agentd.CodeFailed, response.Error)
	}

	// The daemon must still serve requests after recovering from the panic.
	response, err = client.Do(ctx, "echo", map[string]string{"alive": "yes"})
	if err != nil {
		t.Fatalf("the agent must stay alive after a panic: %v", err)
	}
	if !response.OK || response.Stdout != `{"alive":"yes"}` {
		t.Fatalf("unexpected follow-up response %+v", response)
	}
}

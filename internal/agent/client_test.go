package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/agentd"
)

// TestClientSurfacesAgentErrorMessage pins the operator contract: a rejected
// operation keeps its code, and the daemon's concrete reason reaches the
// caller instead of collapsing into a bare AGENT_OP_FAILED.
func TestClientSurfacesAgentErrorMessage(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	const reason = "check opendkim: fork/exec /usr/sbin/opendkim: no such file or directory"
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		line, readErr := bufio.NewReader(conn).ReadBytes('\n')
		if readErr != nil {
			return
		}
		var request agentd.Request
		if json.Unmarshal(line, &request) != nil {
			return
		}
		_ = json.NewEncoder(conn).Encode(agentd.Response{
			Version: agentd.ProtocolVersion,
			ID:      request.ID,
			Error:   &agentd.Error{Code: agentd.CodeFailed, Message: reason},
		})
	}()

	client := NewClient(socket)
	_, err = client.Do(context.Background(), agentd.OpConfigApply, struct{}{})
	if err == nil {
		t.Fatal("rejected operation returned no error")
	}
	if !strings.Contains(err.Error(), agentd.CodeFailed) {
		t.Fatalf("error %q lost the code %s", err, agentd.CodeFailed)
	}
	if !strings.Contains(err.Error(), reason) {
		t.Fatalf("error %q lost the daemon reason", err)
	}
}

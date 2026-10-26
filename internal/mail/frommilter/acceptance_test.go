package frommilter

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/frombind"
)

// TestFrombindServerAcceptance is executed only inside the disposable,
// network-isolated acceptance container (scripts drive it through the
// URMAIL_FROMBIND_ACCEPTANCE switch): it runs the milter server on
// 127.0.0.1:12302 against a fixed sender-login map while the outer script
// probes real Postfix submission on 587. Assertions live in the shell probe,
// which observes the Postfix-level accept/reject outcomes end to end.
func TestFrombindServerAcceptance(t *testing.T) {
	if os.Getenv("URMAIL_FROMBIND_ACCEPTANCE") != "1" {
		t.Skip("URMAIL_FROMBIND_ACCEPTANCE is not set; skipping the container acceptance server")
	}
	const mapRaw = "alice@a.test\talice@a.test\n"
	mapPath := "/tmp/frombind-acceptance.map"
	if err := os.WriteFile(mapPath, []byte(mapRaw), 0o644); err != nil {
		t.Fatal(err)
	}
	authorizer := frombind.MapFileAuthorizer{Path: mapPath}
	DebugLog = func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "frombind: "+format+"\n", args...)
	}
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:12302")
	if err != nil {
		t.Fatal(err)
	}
	if err := Serve(l, authorizer); err != nil {
		t.Logf("frombind server stopped: %v", err)
	}
}

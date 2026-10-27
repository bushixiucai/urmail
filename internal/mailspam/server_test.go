package mailspam

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bushixiucai/urmail/internal/telemetry"
)

// bindPortForTest occupies an ephemeral loopback port and releases it, so a
// test can attempt to bind the same address again.
func bindPortForTest(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestServerServesAndReportsAvailable(t *testing.T) {
	addr := bindPortForTest(t)

	server := &Server{Addr: addr, Handler: staticHandler(VerdictResponse{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe(ctx) }()

	// Wait for the listener to accept at least one probe.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr); err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !server.Running() {
		t.Fatal("server not running after ListenAndServe")
	}
	if got := testutil.ToFloat64(milterAvailable); got != 1 {
		t.Fatalf("availability gauge = %v while serving", got)
	}
	if severity := telemetry.AlertSeverity(AlertKind); severity != telemetry.SeverityWarning {
		t.Fatalf("alert severity = %q, want warning (mail keeps flowing)", severity)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("expected context error on shutdown")
	}
	if got := testutil.ToFloat64(milterAvailable); got != 0 {
		t.Fatalf("availability gauge = %v after shutdown", got)
	}
}

func TestServerBindFailurePublishesAlert(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	// A second bind on the same port must fail and raise the alert: the
	// generated Postfix configuration references this endpoint, so a bind
	// failure means silent unflagged delivery unless the alert fires.
	server := &Server{Addr: ln.Addr().String(), Handler: staticHandler(VerdictResponse{})}
	if err := server.ListenAndServe(context.Background()); err == nil {
		t.Fatal("double bind unexpectedly succeeded")
	}
	if got := testutil.ToFloat64(milterAvailable); got != 0 {
		t.Fatalf("availability gauge = %v after bind failure", got)
	}
	if server.Running() {
		t.Fatal("server reports running after failed bind")
	}
}

func TestServerIdleTimeoutBoundsStuckClients(t *testing.T) {
	addr := bindPortForTest(t)
	server := &Server{Addr: addr, Handler: staticHandler(VerdictResponse{}), IdleTimeout: 150 * time.Millisecond}
	go func() { _ = server.ListenAndServe(context.Background()) }()
	var conn net.Conn
	deadline := time.Now().Add(5 * time.Second)
	for {
		var dialErr error
		if conn, dialErr = (&net.Dialer{}).DialContext(t.Context(), "tcp", addr); dialErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never became reachable: %v", dialErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer func() { _ = conn.Close() }()
	start := time.Now()
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("idle client not cut: err=%v elapsed=%v", err, time.Since(start))
	}
}

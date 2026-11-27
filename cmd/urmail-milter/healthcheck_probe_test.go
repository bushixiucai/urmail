package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/mailspam"
)

// probeHandler satisfies the VerdictHandler interface; the healthcheck probe
// only exercises the OPTNEG handshake and never reaches Decide.
type probeHandler struct{}

func (probeHandler) Decide(_ context.Context, _ mailspam.VerdictRequest) mailspam.VerdictResponse {
	return mailspam.VerdictResponse{}
}

// startLiveSigner binds a real milter Server on an ephemeral loopback port
// and returns its address once dialable.
func startLiveSigner(t *testing.T) string {
	t.Helper()
	reserve, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserve.Addr().(*net.TCPAddr).Port
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	_ = reserve.Close()

	server := &mailspam.Server{Addr: addr, Handler: probeHandler{}}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() { _ = server.ListenAndServe(ctx) }()

	dialer := &net.Dialer{Timeout: time.Second}
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, dialErr := dialer.DialContext(t.Context(), "tcp", addr)
		if dialErr == nil {
			_ = conn.Close()
			return addr
		}
		if time.Now().After(deadline) {
			t.Fatalf("probe signer never became dialable on %s: %v", addr, dialErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// deadLoopbackAddr returns a loopback address whose listener is already
// closed, so connects are refused.
func deadLoopbackAddr(t *testing.T) string {
	t.Helper()
	reserve, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := reserve.Addr().String()
	_ = reserve.Close()
	return addr
}

// TestRunHealthcheckLiveAndDead pins the healthcheck contract against a REAL
// mailspam.Server: rc=0 (nil) only when a well-framed SMFIC_OPTNEG gets a
// valid OPTNEG reply, rc=1 (error) on a refusing port. This closes the
// rev-6 blind spots — the old probe wrote the frame length after the 'O'
// byte, every live signer reset the connection (rc=1, QA S-1 binary probe),
// and no CI step ever invoked the healthcheck subcommand.
func TestRunHealthcheckLiveAndDead(t *testing.T) {
	addr := startLiveSigner(t)
	if err := runHealthcheck(addr); err != nil {
		t.Fatalf("healthcheck against a live signer must pass: %v", err)
	}
	if err := runHealthcheck(deadLoopbackAddr(t)); err == nil {
		t.Fatal("healthcheck against a dead port must fail")
	}
}

// TestHealthcheckBinaryProbe drives the REAL binary subcommand the compose
// HEALTHCHECK runs: `urmail-milter healthcheck` must exit 0 against a live
// signer and 1 against a refused connection.
func TestHealthcheckBinaryProbe(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the real binary")
	}
	bin := filepath.Join(t.TempDir(), "urmail-milter")
	buildCtx, buildCancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer buildCancel()
	// #nosec G204 -- the -o target is a t.TempDir created above; "go" is a constant.
	build := exec.CommandContext(buildCtx, "go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build probe binary: %v\n%s", err, out)
	}

	runProbe := func(addr string) int {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		// #nosec G204 -- the binary path is the t.TempDir build output above.
		cmd := exec.CommandContext(ctx, bin, "healthcheck")
		cmd.Env = append(os.Environ(), "URMAIL_MILTER_LISTEN="+addr)
		err := cmd.Run()
		if err == nil {
			return 0
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		return -1
	}

	if rc := runProbe(startLiveSigner(t)); rc != 0 {
		t.Fatalf("binary healthcheck vs live signer rc=%d, want 0", rc)
	}
	if rc := runProbe(deadLoopbackAddr(t)); rc != 1 {
		t.Fatalf("binary healthcheck vs dead port rc=%d, want 1", rc)
	}
}

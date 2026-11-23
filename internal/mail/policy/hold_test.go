package policy_test

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/mail/policy"
)

// holdCountingQuota records whether a held deployment charged or consulted quota.
type holdCountingQuota struct{ calls int }

func (c *holdCountingQuota) CheckAndIncrement(context.Context, string) (bool, error) {
	c.calls++
	return true, nil
}

// holdInbound records whether a deferred deployment consulted entitlement.
type holdInbound struct{ calls int }

func (c *holdInbound) CheckRecipient(context.Context, string) (bool, error) {
	c.calls++
	return true, nil
}

func holdMarker(t *testing.T) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, "upgrade.hold")
	if err := os.WriteFile(path, []byte(`{"kind":"test-hold"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func TestHoldGateOnlyOpensOnProvenAbsence(t *testing.T) {
	dir, path := holdMarker(t)
	if got := (policy.HoldGate{Path: path}).State(); got != policy.HoldActive {
		t.Fatalf("live marker state=%v want HoldActive", got)
	}
	if got := (policy.HoldGate{Path: ""}).State(); got != policy.HoldOpen {
		t.Fatalf("empty path state=%v want HoldOpen (gate disabled)", got)
	}
	if got := (policy.HoldGate{Path: "upgrade.hold"}).State(); got != policy.HoldUnknown {
		t.Fatalf("relative path state=%v want HoldUnknown", got)
	}
	if got := (policy.HoldGate{Path: filepath.Join(dir, "absent.hold")}).State(); got != policy.HoldOpen {
		t.Fatalf("absent marker in trusted dir state=%v want HoldOpen", got)
	}
	// Abnormal marker types are never a clean absence.
	if got := (policy.HoldGate{Path: dir}).State(); got != policy.HoldUnknown {
		t.Fatalf("directory marker state=%v want HoldUnknown", got)
	}
	link := filepath.Join(dir, "link.hold")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if got := (policy.HoldGate{Path: link}).State(); got != policy.HoldUnknown {
		t.Fatalf("symlink marker state=%v want HoldUnknown", got)
	}
	fifo := filepath.Join(dir, "fifo.hold")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := (policy.HoldGate{Path: fifo}).State(); got != policy.HoldUnknown {
		t.Fatalf("fifo marker state=%v want HoldUnknown", got)
	}
	// Missing or untrusted parents cannot prove absence.
	if got := (policy.HoldGate{Path: filepath.Join(dir, "missing-parent", "upgrade.hold")}).State(); got != policy.HoldUnknown {
		t.Fatalf("missing parent state=%v want HoldUnknown", got)
	}
	notDir := filepath.Join(dir, "parent-file")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := (policy.HoldGate{Path: filepath.Join(notDir, "upgrade.hold")}).State(); got != policy.HoldUnknown {
		t.Fatalf("non-directory parent state=%v want HoldUnknown", got)
	}
	symlinkParent := filepath.Join(dir, "parent-link")
	if err := os.Symlink(dir, symlinkParent); err != nil {
		t.Fatal(err)
	}
	if got := (policy.HoldGate{Path: filepath.Join(symlinkParent, "absent.hold")}).State(); got != policy.HoldUnknown {
		t.Fatalf("symlinked parent state=%v want HoldUnknown", got)
	}
	// An unsearchable trusted directory proves nothing either. Root bypasses
	// permission checks, so this branch is only meaningful unprivileged.
	if os.Geteuid() != 0 {
		locked := filepath.Join(dir, "locked")
		if err := os.Mkdir(locked, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
		if got := (policy.HoldGate{Path: filepath.Join(locked, "upgrade.hold")}).State(); got != policy.HoldUnknown {
			t.Fatalf("unsearchable parent state=%v want HoldUnknown", got)
		}
	}
}

func TestHeldDeploymentDefersBeforeAnyAccounting(t *testing.T) {
	dir, path := holdMarker(t)
	link := filepath.Join(dir, "link.hold")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	abnormal := filepath.Join(dir, "abnormal.hold")
	if err := os.Mkdir(abnormal, 0o700); err != nil {
		t.Fatal(err)
	}
	unsearchable := filepath.Join(dir, "locked")
	if err := os.Mkdir(unsearchable, 0o700); err != nil {
		t.Fatal(err)
	}
	requests := []*policy.Request{
		{ClientAddress: "192.0.2.10", Recipient: "bob@example.test", Attrs: map[string]string{"protocol_state": "RCPT"}},
		{SASLUsername: "alice@example.test", QueueID: "queue-held-1", Attrs: map[string]string{"protocol_state": "DATA"}},
	}
	for _, tc := range []struct {
		name string
		path string
		skip bool
	}{
		{"live marker", path, false},
		{"directory marker", abnormal, false},
		{"symlink marker", link, false},
		{"unsearchable parent", filepath.Join(unsearchable, "upgrade.hold"), os.Geteuid() == 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skip {
				t.Skip("root bypasses directory permissions")
			}
			if tc.name == "unsearchable parent" {
				if err := os.Chmod(unsearchable, 0o000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(unsearchable, 0o700) })
			}
			quota := &holdCountingQuota{}
			inbound := &holdInbound{}
			svc := &policy.Service{Hold: policy.HoldGate{Path: tc.path}, InboundChecker: inbound, QuotaChecker: quota, RateLimiter: policy.NewRateLimiter(1, time.Minute)}
			for _, req := range requests {
				d := svc.Evaluate(context.Background(), req)
				if d.Action != policy.ActionDefer || d.Reason != policy.HoldReason {
					t.Fatalf("request decided %+v, want DEFER with the fixed hold reason", d)
				}
			}
			if quota.calls != 0 || inbound.calls != 0 {
				t.Fatalf("deferred deployment consulted quota=%d inbound=%d, want no writes or lookups", quota.calls, inbound.calls)
			}
		})
	}

	// Allow control: a trusted directory with a proven absent marker opens
	// mail and quota accounting runs exactly once for the message.
	quota := &holdCountingQuota{}
	svc := &policy.Service{Hold: policy.HoldGate{Path: filepath.Join(dir, "absent.hold")}, QuotaChecker: quota, RateLimiter: policy.NewRateLimiter(5, time.Minute)}
	if d := svc.Evaluate(context.Background(), &policy.Request{SASLUsername: "alice@example.test", QueueID: "queue-open-1", Attrs: map[string]string{"protocol_state": "DATA"}}); d.Action != policy.ActionDunno {
		t.Fatalf("proven absence decided %+v, want DUNNO", d)
	}
	if quota.calls != 1 {
		t.Fatalf("open deployment quota calls=%d want 1", quota.calls)
	}
}

// TestHoldOverUnixSocket proves the live policy socket answers Postfix with a
// DEFER_IF_PERMIT carrying the 4xx reason while the marker exists and returns
// DUNNO once urmailctl releases it, without restarting the service.
func TestHoldOverUnixSocket(t *testing.T) {
	dir, path := holdMarker(t)
	sockPath := filepath.Join(dir, "policy.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := policy.NewServer(sockPath, &policy.Service{Hold: policy.HoldGate{Path: path}})
	errCh := make(chan error, 1)
	go func() { errCh <- server.Start(ctx) }()
	t.Cleanup(func() { _ = server.Close() })

	var conn net.Conn
	var err error
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err = (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial policy socket: %v", err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	decide := func(state string) string {
		t.Helper()
		request := "request=smtpd_access_policy\nprotocol_state=" + state + "\nclient_address=192.0.2.10\nrecipient=alice@example.test\nqueue_id=queue-1\n\n"
		if _, err := conn.Write([]byte(request)); err != nil {
			t.Fatalf("write policy request: %v", err)
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read policy decision: %v", err)
		}
		blank, err := reader.ReadString('\n')
		if err != nil || strings.TrimSpace(blank) != "" {
			t.Fatalf("policy decision not terminated: %q %v", blank, err)
		}
		return strings.TrimSpace(line)
	}
	want := "action=DEFER_IF_PERMIT " + policy.HoldReason
	if got := decide("RCPT"); got != want {
		t.Fatalf("held decision %q, want %q", got, want)
	}
	// An abnormal marker type is not a clean absence: the live socket keeps
	// deferring instead of silently accepting mail.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := decide("RCPT"); got != want {
		t.Fatalf("abnormal marker decision %q, want %q", got, want)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := decide("RCPT"); got != "action=DUNNO" {
		t.Fatalf("released decision %q, want action=DUNNO", got)
	}
}

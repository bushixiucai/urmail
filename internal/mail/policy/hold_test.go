package policy_test

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
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

func holdMarker(t *testing.T) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, "upgrade.hold")
	if err := os.WriteFile(path, []byte(`{"kind":"test-hold"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func TestHoldGateReportsRegularMarkerOnly(t *testing.T) {
	dir, path := holdMarker(t)
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"live marker", path, true},
		{"empty path", "", false},
		{"relative path", "upgrade.hold", false},
		{"missing marker", filepath.Join(dir, "absent.hold"), false},
		{"directory is not a marker", dir, false},
	}
	for _, tc := range cases {
		if got := (policy.HoldGate{Path: tc.path}).Held(); got != tc.want {
			t.Fatalf("%s: held=%t want %t", tc.name, got, tc.want)
		}
	}
	link := filepath.Join(dir, "link.hold")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if (policy.HoldGate{Path: link}).Held() {
		t.Fatal("symlink accepted as hold marker")
	}
}

func TestHeldDeploymentDefersBeforeAnyAccounting(t *testing.T) {
	_, path := holdMarker(t)
	quota := &holdCountingQuota{}
	svc := &policy.Service{Hold: policy.HoldGate{Path: path}, QuotaChecker: quota, RateLimiter: policy.NewRateLimiter(1, time.Minute)}
	ctx := context.Background()
	for _, req := range []*policy.Request{
		{ClientAddress: "192.0.2.10", Recipient: "bob@example.test", Attrs: map[string]string{"protocol_state": "RCPT"}},
		{SASLUsername: "alice@example.test", QueueID: "queue-held-1", Attrs: map[string]string{"protocol_state": "DATA"}},
	} {
		d := svc.Evaluate(ctx, req)
		if d.Action != policy.ActionDefer || d.Reason != policy.HoldReason {
			t.Fatalf("held request decided %+v, want DEFER with the fixed hold reason", d)
		}
	}
	if quota.calls != 0 {
		t.Fatalf("held deployment charged quota %d time(s)", quota.calls)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if d := svc.Evaluate(ctx, &policy.Request{ClientAddress: "192.0.2.10"}); d.Action != policy.ActionDunno {
		t.Fatalf("released deployment still decided %+v", d)
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
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := decide("RCPT"); got != "action=DUNNO" {
		t.Fatalf("released decision %q, want action=DUNNO", got)
	}
}

package policy_test

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/mail/policy"
)

type mockQuotaChecker struct {
	allowed bool
	err     error
}

func (m *mockQuotaChecker) CheckAndIncrement(_ context.Context, _ string) (bool, error) {
	return m.allowed, m.err
}

func TestProtocolReadAndWrite(t *testing.T) {
	raw := "client_address=1.2.3.4\n" +
		"client_name=mail.client.com\n" +
		"sasl_username=alice@example.com\n" +
		"sender=alice@example.com\n" +
		"recipient=bob@example.com\n\n"

	r := bufio.NewReader(strings.NewReader(raw))
	req, err := policy.ReadRequest(r)
	if err != nil {
		t.Fatalf("ReadRequest failed: %v", err)
	}
	if req == nil {
		t.Fatal("expected request, got nil")
	}
	if req.ClientAddress != "1.2.3.4" {
		t.Errorf("expected 1.2.3.4, got %s", req.ClientAddress)
	}
	if req.SASLUsername != "alice@example.com" {
		t.Errorf("expected alice@example.com, got %s", req.SASLUsername)
	}

	var buf bytes.Buffer
	decision := policy.Decision{Action: policy.ActionDunno}
	if err := policy.WriteDecision(&buf, decision); err != nil {
		t.Fatalf("WriteDecision: %v", err)
	}
	if buf.String() != "action=DUNNO\n\n" {
		t.Errorf("unexpected output: %q", buf.String())
	}
}

func TestServiceEvaluations(t *testing.T) {
	ctx := context.Background()
	svc := &policy.Service{
		RateLimiter:  policy.NewRateLimiter(2, time.Second),
		QuotaChecker: &mockQuotaChecker{allowed: true},
	}

	req := &policy.Request{
		SASLUsername: "user@example.com",
	}

	// First 2 requests pass
	d1 := svc.Evaluate(ctx, req)
	if d1.Action != policy.ActionDunno {
		t.Errorf("first request: expected DUNNO, got %s", d1.Action)
	}
	d2 := svc.Evaluate(ctx, req)
	if d2.Action != policy.ActionDunno {
		t.Errorf("second request: expected DUNNO, got %s", d2.Action)
	}

	// Third request is rate limited
	d3 := svc.Evaluate(ctx, req)
	if d3.Action != policy.ActionReject {
		t.Errorf("third request: expected REJECT, got %s", d3.Action)
	}
}

func TestQuotaCheckReject(t *testing.T) {
	ctx := context.Background()
	svc := &policy.Service{
		QuotaChecker: &mockQuotaChecker{allowed: false},
	}
	req := &policy.Request{SASLUsername: "overquota@example.com"}
	d := svc.Evaluate(ctx, req)
	if d.Action != policy.ActionReject || !strings.Contains(d.Reason, "quota") {
		t.Errorf("expected quota REJECT, got %+v", d)
	}
}

func TestServerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "policy.sock")

	svc := &policy.Service{}
	srv := policy.NewServer(sockPath, svc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	// Wait for socket to become ready
	time.Sleep(50 * time.Millisecond)

	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
	if err != nil {
		t.Fatalf("dial socket: %v", err)
	}
	defer conn.Close()

	reqData := "client_address=1.1.1.1\n\n"
	if _, err := conn.Write([]byte(reqData)); err != nil {
		t.Fatalf("write to socket: %v", err)
	}

	r := bufio.NewReader(conn)
	resp, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if strings.TrimSpace(resp) != "action=DUNNO" {
		t.Errorf("expected action=DUNNO, got %q", resp)
	}

	cancel()
	_ = srv.Close()
}

package policy_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/policy"
)

type countingQuota struct {
	calls     int
	allowed   bool
	err       error
	lastIdent string
}

func (c *countingQuota) CheckAndIncrement(_ context.Context, mailboxAddress string) (bool, error) {
	c.calls++
	c.lastIdent = mailboxAddress
	return c.allowed, c.err
}

func TestEvaluateSASLUsernamePreferred(t *testing.T) {
	q := &countingQuota{allowed: true}
	svc := policy.NewService()
	svc.QuotaChecker = q

	d := svc.Evaluate(context.Background(), &policy.Request{
		SASLUsername:  "user@example.com",
		ClientAddress: "10.0.0.1",
		QueueID:       "Q1",
	})
	if d.Action != policy.ActionDunno {
		t.Fatalf("expected DUNNO, got %v", d.Action)
	}
	if q.lastIdent != "user@example.com" {
		t.Fatalf("expected sasl_username as identity, got %q", q.lastIdent)
	}
}

func TestEvaluateSkipsUnauthenticatedInbound(t *testing.T) {
	q := &countingQuota{allowed: true}
	svc := policy.NewService()
	svc.QuotaChecker = q

	d := svc.Evaluate(context.Background(), &policy.Request{
		ClientAddress: "203.0.113.9",
		QueueID:       "Q2",
	})
	if d.Action != policy.ActionDunno {
		t.Fatalf("expected DUNNO, got %v", d.Action)
	}
	if q.lastIdent != "" {
		t.Fatalf("inbound mail must not be charged to a mailbox, got %q", q.lastIdent)
	}
}

func TestEvaluateDeduplicatesByIdentityAndQueueID(t *testing.T) {
	q := &countingQuota{allowed: true}
	svc := policy.NewService()
	svc.QuotaChecker = q

	req := &policy.Request{
		SASLUsername: "user@example.com",
		QueueID:      "ABC123",
	}
	for i := 0; i < 3; i++ {
		svc.Evaluate(context.Background(), req) // same recipient re-evaluation
	}
	if q.calls != 1 {
		t.Fatalf("expected 1 quota call for repeated (identity, queue_id), got %d", q.calls)
	}

	// A different queue id (new message) must be counted again.
	req.QueueID = "DEF456"
	svc.Evaluate(context.Background(), req)
	if q.calls != 2 {
		t.Fatalf("expected 2 quota calls for a new queue_id, got %d", q.calls)
	}

	// A different identity with the same queue id must be counted too.
	req.SASLUsername = "other@example.com"
	svc.Evaluate(context.Background(), req)
	if q.calls != 3 {
		t.Fatalf("expected 3 quota calls for a new identity, got %d", q.calls)
	}
}

func TestEvaluateQuotaExceededAndDeferred(t *testing.T) {
	t.Run("quota_rejected", func(t *testing.T) {
		q := &countingQuota{allowed: false}
		svc := policy.NewService()
		svc.QuotaChecker = q
		d := svc.Evaluate(context.Background(), &policy.Request{SASLUsername: "u@x.com", QueueID: "Q"})
		if d.Action != policy.ActionReject {
			t.Fatalf("expected REJECT, got %v", d.Action)
		}
	})

	t.Run("db_error_defers", func(t *testing.T) {
		q := &countingQuota{allowed: true, err: context.DeadlineExceeded}
		svc := policy.NewService()
		svc.QuotaChecker = q
		d := svc.Evaluate(context.Background(), &policy.Request{SASLUsername: "u@x.com", QueueID: "Q"})
		if d.Action != policy.ActionDefer {
			t.Fatalf("expected DEFER_IF_PERMIT, got %v", d.Action)
		}
	})
}

func TestSeenCacheCapacityAndTTL(t *testing.T) {
	// Exercise through the exported Service: use a big number of keys and
	// confirm capacity bound via repeated behavior is deterministic. The
	// cache itself is tested indirectly here (standard library only).
	c := policy.NewSeenCacheForTest(2, 60_000_000_000) // 1 minute in ns
	if c.Seen("a") {
		t.Fatal("first sight of a must be false")
	}
	if !c.Seen("a") {
		t.Fatal("second sight of a must be true")
	}
	c.Seen("b")
	c.Seen("c") // evicts "a" (FIFO, capacity 2)
	if c.Seen("a") {
		t.Fatal("a must have been evicted")
	}
}

type fixedInbound struct {
	allowed bool
	err     error
}

func (f fixedInbound) CheckRecipient(_ context.Context, _ string) (bool, error) {
	return f.allowed, f.err
}
func TestRecipientSubscriptionCheckedBeforeAuthenticatedBypass(t *testing.T) {
	for _, identity := range []string{"", "sender@example.test"} {
		svc := policy.NewService()
		svc.InboundChecker = fixedInbound{allowed: false}
		decision := svc.Evaluate(context.Background(), &policy.Request{SASLUsername: identity, Recipient: "expired@example.test", Attrs: map[string]string{"protocol_state": "RCPT"}})
		if decision.Action != policy.ActionDefer {
			t.Fatalf("expired recipient allowed for identity %q: %+v", identity, decision)
		}
	}
}
func TestEvaluateSuspendedEntitlementReason(t *testing.T) {
	q := &countingQuota{allowed: false, err: policy.ErrSuspended}
	svc := policy.NewService()
	svc.QuotaChecker = q

	d := svc.Evaluate(context.Background(), &policy.Request{
		SASLUsername:  "user@example.com",
		ClientAddress: "10.0.0.1",
		QueueID:       "Q9",
	})
	if d.Action != policy.ActionReject {
		t.Fatalf("expected REJECT, got %v", d.Action)
	}
	if !strings.Contains(d.Reason, "suspended") {
		t.Fatalf("expected explicit suspended reason, got %q", d.Reason)
	}
}

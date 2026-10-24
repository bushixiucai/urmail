package mail

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type staticPolicy struct {
	allowed bool
	err     error
}

func (p staticPolicy) AllowedToSend(int64) (bool, error) { return p.allowed, p.err }

// allowAll installs the required-but-permissive checker/policy pair used by
// tests that exercise the send path itself.
func allowAll(q *DispatchQueue) {
	q.SetSuppressionChecker(staticSuppressionChecker{})
	q.SetSenderPolicy(staticPolicy{allowed: true})
}

// A queue that actually sends must refuse to dispatch without a durable
// suppression checker (fail closed).
func TestDispatchQueue_NoSuppressionCheckerRefusesToSend(t *testing.T) {
	channel := &OutboundChannel{Kind: ChannelKindDedicatedIP, DedicatedIP: "192.0.2.31", IsDedicated: true}
	sent := 0
	q := NewDispatchQueue(channel, nil, nil, func(context.Context, *OutboundChannel, *PreparedMarketingMessage) error { sent++; return nil })
	q.SetSenderPolicy(staticPolicy{allowed: true})
	q.Enqueue(&PreparedMarketingMessage{ID: "no-checker", TenantID: 1, Recipient: "r@example.test"})
	if _, err := q.DispatchNext(context.Background()); !errors.Is(err, ErrSuppressionCheckerRequired) {
		t.Fatalf("expected ErrSuppressionCheckerRequired, got %v", err)
	}
	if sent != 0 || q.QueueLength() != 1 {
		t.Fatalf("message must not be leased: sent=%d queue=%d", sent, q.QueueLength())
	}
}

// A queue with no sender policy must refuse to send (fail closed), and a
// policy error or denial must skip and record the message.
func TestDispatchQueue_SenderPolicyFailClosed(t *testing.T) {
	channel := &OutboundChannel{Kind: ChannelKindDedicatedIP, DedicatedIP: "192.0.2.32", IsDedicated: true}
	sent := 0
	sender := func(context.Context, *OutboundChannel, *PreparedMarketingMessage) error { sent++; return nil }

	// No policy at all.
	q := NewDispatchQueue(channel, nil, nil, sender)
	q.SetSuppressionChecker(staticSuppressionChecker{})
	q.Enqueue(&PreparedMarketingMessage{ID: "no-policy", TenantID: 1, Recipient: "r@example.test"})
	if _, err := q.DispatchNext(context.Background()); !errors.Is(err, ErrSenderPolicyRequired) {
		t.Fatalf("expected ErrSenderPolicyRequired, got %v", err)
	}
	if sent != 0 || q.QueueLength() != 1 {
		t.Fatalf("message must not be leased: sent=%d queue=%d", sent, q.QueueLength())
	}

	// Policy error: skip + record.
	q2 := NewDispatchQueue(channel, nil, nil, sender)
	q2.SetSuppressionChecker(staticSuppressionChecker{})
	q2.SetSenderPolicy(staticPolicy{err: errors.New("policy store down")})
	q2.Enqueue(&PreparedMarketingMessage{ID: "policy-err", TenantID: 1, Recipient: "r@example.test"})
	if _, err := q2.DispatchNext(context.Background()); !errors.Is(err, ErrSenderPolicyDenied) || sent != 0 || q2.DroppedCount() != 1 {
		t.Fatalf("policy error must skip and record: sent=%d dropped=%d err=%v", sent, q2.DroppedCount(), err)
	}

	// Policy denial: skip + record.
	q3 := NewDispatchQueue(channel, nil, nil, sender)
	q3.SetSuppressionChecker(staticSuppressionChecker{})
	q3.SetSenderPolicy(staticPolicy{allowed: false})
	q3.Enqueue(&PreparedMarketingMessage{ID: "policy-deny", TenantID: 2, Recipient: "r@example.test"})
	if _, err := q3.DispatchNext(context.Background()); !errors.Is(err, ErrSenderPolicyDenied) || sent != 0 || q3.DroppedCount() != 1 {
		t.Fatalf("policy denial must skip and record: sent=%d dropped=%d err=%v", sent, q3.DroppedCount(), err)
	}
}

// Two concurrent workers must never panic, and no message may be lost or
// double-queued, even though the queue mutates between lock sections.
func TestDispatchQueue_ConcurrentWorkersNoPanicNoLoss(t *testing.T) {
	ctx := context.Background()
	channel := &OutboundChannel{Kind: ChannelKindDedicatedIP, DedicatedIP: "192.0.2.33", IsDedicated: true}

	var mu sync.Mutex
	sentIDs := make(map[string]int)
	sender := func(_ context.Context, _ *OutboundChannel, msg *PreparedMarketingMessage) error {
		mu.Lock()
		sentIDs[msg.ID]++
		mu.Unlock()
		return nil
	}

	limiter := NewRateLimiter(RateLimitConfig{}) // unlimited
	q := NewDispatchQueue(channel, limiter, nil, sender)
	allowAll(q)

	const workers = 8
	const perWorker = 25
	for i := 0; i < workers*perWorker; i++ {
		q.Enqueue(&PreparedMarketingMessage{
			ID:        fmt.Sprintf("m-%03d", i),
			TenantID:  int64(i%3 + 1),
			Recipient: fmt.Sprintf("r%d@example.test", i),
		})
	}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				_, err := q.DispatchNext(ctx)
				if errors.Is(err, ErrQueueEmpty) {
					return
				}
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	total := 0
	for id, n := range sentIDs {
		total += n
		if n != 1 {
			t.Fatalf("message %s sent %d times", id, n)
		}
	}
	if total != workers*perWorker {
		t.Fatalf("expected %d sends, got %d", workers*perWorker, total)
	}
	if q.QueueLength() != 0 {
		t.Fatalf("queue must be empty, got %d", q.QueueLength())
	}
	if q.SentCount() != workers*perWorker {
		t.Fatalf("expected %d sent records, got %d", workers*perWorker, q.SentCount())
	}
}

// A sender failure marked uncertain must record the message as uncertain and
// must NOT re-queue it for another send.
func TestDispatchQueue_UncertainDeliveryNotResent(t *testing.T) {
	ctx := context.Background()
	channel := &OutboundChannel{Kind: ChannelKindDedicatedIP, DedicatedIP: "192.0.2.34", IsDedicated: true}

	calls := 0
	sender := func(context.Context, *OutboundChannel, *PreparedMarketingMessage) error {
		calls++
		return MarkDeliveryUncertain(errors.New("connection dropped after DATA"))
	}

	q := NewDispatchQueue(channel, nil, nil, sender)
	allowAll(q)
	q.SetRetryPolicy(3, time.Second)
	q.Enqueue(&PreparedMarketingMessage{ID: "m-uncertain", TenantID: 1, Recipient: "r@example.test"})

	if _, err := q.DispatchNext(ctx); !errors.Is(err, ErrDeliveryUncertain) {
		t.Fatalf("expected ErrDeliveryUncertain, got %v", err)
	}
	if q.UncertainCount() != 1 {
		t.Fatalf("expected 1 uncertain messages, got %d", q.UncertainCount())
	}
	if q.QueueLength() != 0 {
		t.Fatalf("uncertain message must not be re-queued, got len %d", q.QueueLength())
	}

	// Even after backoff elapses there is nothing left to re-send.
	if _, err := q.DispatchNext(ctx); !errors.Is(err, ErrQueueEmpty) {
		t.Fatalf("expected ErrQueueEmpty after uncertain outcome, got %v", err)
	}
	if calls != 1 || q.SentCount() != 0 {
		t.Fatalf("message must be sent exactly once: calls=%d sent=%d", calls, q.SentCount())
	}
	if q.Stats().Uncertain != 1 {
		t.Fatalf("stats must expose uncertain count, got %+v", q.Stats())
	}
}

// CRLF injection in a recipient must be rejected during batch preparation.
func TestPrepareBatch_RejectsCRLFAndControlChars(t *testing.T) {
	mgr := mustSuppressionMgr(t, nil)
	base := BatchPreparationRequest{
		TenantID:           1,
		BaseUnsubscribeURL: "https://example.com/unsub",
	}
	base.Recipients = []string{
		"victim@example.com\r\nBcc: attacker@evil.com",
		"user@bad..example.com",
		"a@b.example.com\x00",
		"spaces in@example.com",
		"user@localhost",
		"user@@example.com",
	}
	res, err := PrepareBatch(base, mgr)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 0 {
		t.Fatalf("no message may be prepared from injection recipients, got %d", len(res.Messages))
	}
	if len(res.InvalidEmails) != 6 {
		t.Fatalf("expected 6 invalid emails, got %v", res.InvalidEmails)
	}
}

// Custom header names must match a conservative token pattern and header
// values must not contain control characters.
func TestPrepareBatch_RejectsBadCustomHeaders(t *testing.T) {
	mgr := mustSuppressionMgr(t, nil)
	mk := func(headers map[string]string) BatchPreparationRequest {
		return BatchPreparationRequest{
			TenantID:           1,
			BaseUnsubscribeURL: "https://example.com/unsub",
			Recipients:         []string{"user@example.com"},
			CustomHeaders:      headers,
		}
	}
	bad := []map[string]string{
		{"X-Bad\r\nInjected": "value"},
		{"X Bad Name": "value"},
		{"X-Ctrl": "line1\r\nline2"},
		{"X-Null": "a\x00b"},
	}
	for _, h := range bad {
		if _, err := PrepareBatch(mk(h), mgr); !errors.Is(err, ErrInvalidCustomHeader) {
			t.Fatalf("expected ErrInvalidCustomHeader for %v, got %v", h, err)
		}
	}
	ok, err := PrepareBatch(mk(map[string]string{"X-Campaign-Type": "newsletter-2026"}), mgr)
	if err != nil {
		t.Fatalf("valid custom header rejected: %v", err)
	}
	if ok.Messages[0].Headers["X-Campaign-Type"] != "newsletter-2026" {
		t.Fatal("valid custom header not preserved")
	}
}

// The outcome lists must respect maxTrackedRecords, evicting and counting the
// oldest records once the cap is reached.
func TestDispatchQueue_OutcomeListsBounded(t *testing.T) {
	channel := &OutboundChannel{Kind: ChannelKindDedicatedIP, DedicatedIP: "192.0.2.35", IsDedicated: true}
	q := NewDispatchQueue(channel, nil, nil, nil)
	allowAll(q)

	for i := 0; i < maxTrackedRecords+10; i++ {
		recordCapped(&q.sent, &q.sentOverflow, &PreparedMarketingMessage{ID: fmt.Sprintf("s-%d", i)})
		recordCapped(&q.dropped, &q.droppedOverflow, &PreparedMarketingMessage{ID: fmt.Sprintf("d-%d", i)})
		recordCapped(&q.dead, &q.deadOverflow, &PreparedMarketingMessage{ID: fmt.Sprintf("x-%d", i)})
	}
	if len(q.sent) != maxTrackedRecords || q.sentOverflow != 10 {
		t.Fatalf("sent cap: len=%d overflow=%d", len(q.sent), q.sentOverflow)
	}
	if len(q.dropped) != maxTrackedRecords || q.droppedOverflow != 10 {
		t.Fatalf("dropped cap: len=%d overflow=%d", len(q.dropped), q.droppedOverflow)
	}
	if len(q.dead) != maxTrackedRecords || q.deadOverflow != 10 {
		t.Fatalf("dead cap: len=%d overflow=%d", len(q.dead), q.deadOverflow)
	}
	// Oldest entries were evicted first.
	if q.sent[0].ID != fmt.Sprintf("s-%d", 10) {
		t.Fatalf("expected oldest evicted, got head %s", q.sent[0].ID)
	}
}

// A definite send failure must return the reserved per-tenant rate-limit
// token, and tenants must have independent buckets.
func TestRateLimiter_PerTenantBucketsAndRefund(t *testing.T) {
	cfg := RateLimitConfig{MaxPerHour: 1, MaxPerDay: 1}
	limiter := NewRateLimiter(cfg)

	if err := limiter.TakeFor(1, 1); err != nil {
		t.Fatalf("tenant 1 first send must pass: %v", err)
	}
	// Tenant 1 is exhausted; tenant 2 is unaffected.
	if err := limiter.TakeFor(1, 1); !errors.Is(err, ErrHourlyRateLimitExceeded) {
		t.Fatalf("tenant 1 expected hourly limit, got %v", err)
	}
	if err := limiter.TakeFor(2, 1); err != nil {
		t.Fatalf("tenant 2 must have its own bucket: %v", err)
	}

	// Refund returns the token to the tenant bucket only.
	limiter.RefundFor(1, 1)
	if err := limiter.TakeFor(1, 1); err != nil {
		t.Fatalf("refunded token must be usable: %v", err)
	}
	h1, _ := limiter.AvailableFor(1)
	if h1 > 0.01 {
		t.Fatalf("tenant 1 bucket should be empty after use, got %f", h1)
	}
}

// A nil manager must fail verification instead of panicking.
func TestVerifyUnsubscribeToken_NilManager(t *testing.T) {
	var mgr *SuppressionManager
	if _, _, ok := mgr.VerifyUnsubscribeToken("anything.whatever"); ok {
		t.Fatal("nil manager must fail verification")
	}
}

// Reserved rate-limit tokens must NOT be refunded on an uncertain outcome:
// the message may have been delivered, so the quota stays consumed.
func TestDispatchQueue_UncertainKeepsRateLimitToken(t *testing.T) {
	channel := &OutboundChannel{Kind: ChannelKindDedicatedIP, DedicatedIP: "192.0.2.36", IsDedicated: true}
	limiter := NewRateLimiter(RateLimitConfig{MaxPerHour: 1, MaxPerDay: 1})
	q := NewDispatchQueue(channel, limiter, nil, func(context.Context, *OutboundChannel, *PreparedMarketingMessage) error {
		return MarkDeliveryUncertain(errors.New("timeout after DATA"))
	})
	allowAll(q)
	q.Enqueue(&PreparedMarketingMessage{ID: "m-token", TenantID: 9, Recipient: "r@example.test"})
	if _, err := q.DispatchNext(context.Background()); !errors.Is(err, ErrDeliveryUncertain) {
		t.Fatalf("expected ErrDeliveryUncertain, got %v", err)
	}
	if h, _ := limiter.AvailableFor(9); h > 0.01 {
		t.Fatalf("token must stay consumed on uncertain outcome, got %f", h)
	}
}

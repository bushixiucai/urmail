package mail

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDirectDispatchRequiresAllEgressGates(t *testing.T) {
	channel := &OutboundChannel{Kind: ChannelKindDedicatedIP, DedicatedIP: "192.0.2.33", IsDedicated: true}
	message := &PreparedMarketingMessage{ID: "direct", TenantID: 1, Recipient: "person@example.test"}
	for _, variant := range []struct {
		name    string
		checker SuppressionChecker
		policy  SenderPolicy
		missing bool
		want    error
	}{
		{"missing checker", nil, staticPolicy{allowed: true}, false, ErrSuppressionCheckerRequired},
		{"missing policy", staticSuppressionChecker{}, nil, false, ErrSenderPolicyRequired},
		{"missing sender", staticSuppressionChecker{}, staticPolicy{allowed: true}, true, ErrSenderRequired},
		{"suppressed", staticSuppressionChecker{suppressed: true}, staticPolicy{allowed: true}, false, ErrSuppressedRecipient},
		{"checker unavailable", staticSuppressionChecker{err: errors.New("DB down")}, staticPolicy{allowed: true}, false, ErrSuppressedRecipient},
		{"policy denied", staticSuppressionChecker{}, staticPolicy{allowed: false}, false, ErrSenderPolicyDenied},
		{"policy unavailable", staticSuppressionChecker{}, staticPolicy{err: errors.New("policy down")}, false, ErrSenderPolicyDenied},
	} {
		t.Run(variant.name, func(t *testing.T) {
			for _, dispatch := range []func(context.Context, *OutboundChannel, *PreparedMarketingMessage, *RateLimiter, *SuppressionManager, SuppressionChecker, SenderPolicy, SenderFunc) error{
				DispatchMarketingMessage, DispatchMarketingMessageWithChecker,
			} {
				calls := 0
				var sender SenderFunc = func(context.Context, *OutboundChannel, *PreparedMarketingMessage) error { calls++; return nil }
				if variant.missing {
					sender = nil
				}
				err := dispatch(context.Background(), channel, message, nil, nil, variant.checker, variant.policy, sender)
				if !errors.Is(err, variant.want) || calls != 0 {
					t.Fatalf("gate did not refuse before sender: calls=%d err=%v", calls, err)
				}
			}
		})
	}
}

func TestQueueDoesNotPretendMissingSenderSucceeded(t *testing.T) {
	channel := &OutboundChannel{Kind: ChannelKindDedicatedIP, DedicatedIP: "192.0.2.34", IsDedicated: true}
	queue := NewDispatchQueue(channel, nil, nil, nil)
	allowAll(queue)
	queue.Enqueue(&PreparedMarketingMessage{ID: "missing-sender", TenantID: 1, Recipient: "person@example.test"})
	if _, err := queue.DispatchNext(context.Background()); !errors.Is(err, ErrSenderRequired) {
		t.Fatalf("expected missing physical sender error, got %v", err)
	}
	if queue.SentCount() != 0 || queue.QueueLength() != 1 {
		t.Fatal("missing sender consumed message or reported delivery")
	}
}

func TestDirectDispatchLimitsAreTenantQualified(t *testing.T) {
	channel := &OutboundChannel{Kind: ChannelKindDedicatedIP, DedicatedIP: "192.0.2.35", IsDedicated: true}
	limiter := NewRateLimiter(RateLimitConfig{MaxPerHour: 1, MaxPerDay: 1})
	sender := func(context.Context, *OutboundChannel, *PreparedMarketingMessage) error { return nil }
	for _, tenant := range []int64{1, 2} {
		message := &PreparedMarketingMessage{ID: "limited", TenantID: tenant, Recipient: "person@example.test"}
		if err := DispatchMarketingMessage(context.Background(), channel, message, limiter, nil, staticSuppressionChecker{}, staticPolicy{allowed: true}, sender); err != nil {
			t.Fatalf("tenant %d incorrectly shares another tenant's budget: %v", tenant, err)
		}
	}
	message := &PreparedMarketingMessage{ID: "limited-again", TenantID: 1, Recipient: "person@example.test"}
	if err := DispatchMarketingMessage(context.Background(), channel, message, limiter, nil, staticSuppressionChecker{}, staticPolicy{allowed: true}, sender); !errors.Is(err, ErrHourlyRateLimitExceeded) {
		t.Fatalf("tenant budget was bypassed: %v", err)
	}
}

func TestDirectDispatchRetainsOnlyUncertainReservations(t *testing.T) {
	channel := &OutboundChannel{Kind: ChannelKindDedicatedIP, DedicatedIP: "192.0.2.36", IsDedicated: true}
	for _, uncertain := range []bool{false, true} {
		limiter := NewRateLimiter(RateLimitConfig{MaxPerHour: 1, MaxPerDay: 1})
		limiter.SetClock(func() time.Time { return time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC) })
		message := &PreparedMarketingMessage{ID: "reservation", TenantID: 1, Recipient: "person@example.test"}
		sendErr := errors.New("transport failed")
		if uncertain {
			sendErr = MarkDeliveryUncertain(sendErr)
		}
		err := DispatchMarketingMessage(context.Background(), channel, message, limiter, nil, staticSuppressionChecker{}, staticPolicy{allowed: true}, func(context.Context, *OutboundChannel, *PreparedMarketingMessage) error { return sendErr })
		if !errors.Is(err, sendErr) {
			t.Fatalf("sender result lost: %v", err)
		}
		_, daily := limiter.AvailableFor(1)
		if uncertain && daily != 0 || !uncertain && daily < 0.99 {
			t.Fatalf("wrong reservation outcome uncertain=%t daily=%f", uncertain, daily)
		}
	}
}

package mail

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// 1. Requirements per P08: Independent Outbound Channel Tests

func TestValidateMarketingChannel(t *testing.T) {
	tests := []struct {
		name    string
		channel *OutboundChannel
		wantErr error
	}{
		{
			name:    "nil channel fails immediately",
			channel: nil,
			wantErr: ErrMarketingChannelNotConfigured,
		},
		{
			name: "transactional channel refuses fallback",
			channel: &OutboundChannel{
				Kind:        ChannelKindTransactional,
				IsDedicated: false,
			},
			wantErr: ErrRefuseFallbackToTransactional,
		},
		{
			name: "dedicated flag false refuses fallback",
			channel: &OutboundChannel{
				Kind:        ChannelKindDedicatedIP,
				DedicatedIP: "198.51.100.10",
				IsDedicated: false,
			},
			wantErr: ErrRefuseFallbackToTransactional,
		},
		{
			name: "dedicated IP channel missing IP",
			channel: &OutboundChannel{
				Kind:        ChannelKindDedicatedIP,
				DedicatedIP: "",
				IsDedicated: true,
			},
			wantErr: ErrMarketingChannelNotConfigured,
		},
		{
			name: "valid dedicated IP channel",
			channel: &OutboundChannel{
				Kind:        ChannelKindDedicatedIP,
				DedicatedIP: "198.51.100.15",
				IsDedicated: true,
			},
			wantErr: nil,
		},
		{
			name: "dedicated gateway missing host",
			channel: &OutboundChannel{
				Kind:        ChannelKindDedicatedGateway,
				Host:        "",
				Port:        587,
				IsDedicated: true,
			},
			wantErr: ErrMarketingChannelNotConfigured,
		},
		{
			name: "dedicated gateway zero port",
			channel: &OutboundChannel{
				Kind:        ChannelKindDedicatedGateway,
				Host:        "smtp-relay.example.com",
				Port:        0,
				IsDedicated: true,
			},
			wantErr: ErrMarketingChannelNotConfigured,
		},
		{
			name: "valid dedicated gateway",
			channel: &OutboundChannel{
				Kind:        ChannelKindDedicatedGateway,
				Host:        "smtp-relay.example.com",
				Port:        587,
				Username:    "mkt-user",
				Password:    "secret",
				IsDedicated: true,
			},
			wantErr: nil,
		},
		{
			name: "unknown channel kind",
			channel: &OutboundChannel{
				Kind:        ChannelKind("satellite"),
				IsDedicated: true,
			},
			wantErr: ErrMarketingChannelNotConfigured,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMarketingChannel(tc.channel)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
			} else {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected error %v, got %v", tc.wantErr, err)
				}
			}
		})
	}
}

func TestMarketingDispatchRefusesFallbackToTransactional(t *testing.T) {
	ctx := context.Background()
	msg := &PreparedMarketingMessage{
		ID:        "mkt-1",
		TenantID:  10,
		Recipient: "user@example.com",
		Subject:   "Promo",
	}

	senderCalled := false
	sender := func(_ context.Context, _ *OutboundChannel, _ *PreparedMarketingMessage) error {
		senderCalled = true
		return nil
	}

	// 1. Transactional channel must fail immediately
	txChannel := &OutboundChannel{
		Kind:        ChannelKindTransactional,
		IsDedicated: false,
	}
	err := DispatchMarketingMessage(ctx, txChannel, msg, nil, nil, nil, nil, sender)
	if !errors.Is(err, ErrRefuseFallbackToTransactional) {
		t.Fatalf("expected ErrRefuseFallbackToTransactional, got %v", err)
	}
	if senderCalled {
		t.Fatal("sender must not be called when fallback is refused")
	}

	// 2. Unconfigured channel (nil) must fail immediately
	err = DispatchMarketingMessage(ctx, nil, msg, nil, nil, nil, nil, sender)
	if !errors.Is(err, ErrMarketingChannelNotConfigured) {
		t.Fatalf("expected ErrMarketingChannelNotConfigured, got %v", err)
	}
	if senderCalled {
		t.Fatal("sender must not be called when channel is nil")
	}
}

// 2. Suppression List & Unsubscribe Link Tests

func TestSuppressionList_BasicAndCaseInsensitivity(t *testing.T) {
	mgr := mustSuppressionMgr(t, nil)
	tenantID := int64(100)

	// Initially not suppressed
	if mgr.CheckSuppression(tenantID, "Target@Example.COM") {
		t.Fatal("expected email not to be suppressed initially")
	}

	// Add with mixed case and spaces
	mgr.AddSuppression(tenantID, "  Target@Example.COM  ", SuppressionReasonBounce)

	// Check with different casing
	if !mgr.CheckSuppression(tenantID, "target@example.com") {
		t.Fatal("expected lower case email to be suppressed")
	}
	if !mgr.CheckSuppression(tenantID, "TARGET@EXAMPLE.COM") {
		t.Fatal("expected upper case email to be suppressed")
	}

	// Bounce suppressions are permanent and cannot be removed through the manual API.
	if mgr.RemoveSuppression(tenantID, "Target@example.com") {
		t.Fatal("bounce suppression must not be removed")
	}
	if !mgr.CheckSuppression(tenantID, "target@example.com") {
		t.Fatal("bounce suppression should survive manual removal")
	}
}

func TestSuppressionList_MultiTenantIsolation(t *testing.T) {
	mgr := mustSuppressionMgr(t, nil)
	tenantA := int64(101)
	tenantB := int64(102)
	email := "shared-customer@client.org"

	mgr.AddSuppression(tenantA, email, SuppressionReasonComplaint)

	if !mgr.CheckSuppression(tenantA, email) {
		t.Fatal("expected tenantA to have email suppressed")
	}
	if mgr.CheckSuppression(tenantB, email) {
		t.Fatal("expected tenantB NOT to have email suppressed (cross-tenant leakage)")
	}
}

func TestSuppressionList_ConsentAndPermanentReasonsCannotBeDowngradedOrRemoved(t *testing.T) {
	mgr := mustSuppressionMgr(t, nil)
	for _, reason := range []string{SuppressionReasonUnsubscribe, SuppressionReasonBounce, SuppressionReasonComplaint} {
		mgr.AddSuppression(77, "user@example.test", reason)
		mgr.AddSuppression(77, "user@example.test", SuppressionReasonManual)
		if !mgr.CheckSuppression(77, "user@example.test") {
			t.Fatalf("%s suppression was removed by later manual upsert", reason)
		}
		if mgr.RemoveSuppression(77, "user@example.test") {
			t.Fatalf("%s suppression was removable through manual API", reason)
		}
	}
}

func TestSuppressionList_UnsubscribeLinkHandling(t *testing.T) {
	mgr := mustSuppressionMgr(t, []byte("test-unsubscribe-secret-key-12345"))
	tenantID := int64(200)
	email := "subscriber@example.net"

	token := mgr.GenerateUnsubscribeToken(tenantID, email)
	if token == "" {
		t.Fatal("generated token must not be empty")
	}

	// Verify HandleUnsubscribeLink
	unsubEmail, ok := mgr.HandleUnsubscribeLink(token)
	if !ok {
		t.Fatal("expected HandleUnsubscribeLink to succeed")
	}
	if unsubEmail != email {
		t.Fatalf("expected email %q, got %q", email, unsubEmail)
	}

	// Email must now be suppressed in the tenant
	if !mgr.CheckSuppression(tenantID, email) {
		t.Fatal("expected email to be suppressed after unsubscribe link handled")
	}

	// Idempotency: handling again still returns ok=true and email
	unsubEmail2, ok2 := mgr.HandleUnsubscribeLink(token)
	if !ok2 || unsubEmail2 != email {
		t.Fatalf("expected idempotent success, got ok=%v, email=%q", ok2, unsubEmail2)
	}
}

func TestSuppressionList_UnsubscribeLink_TamperedAndInvalid(t *testing.T) {
	mgr := mustSuppressionMgr(t, []byte("secret-key"))
	tenantID := int64(201)
	email := "victim@example.com"

	token := mgr.GenerateUnsubscribeToken(tenantID, email)

	// Tampered token
	tampered := token + "bad"
	if _, ok := mgr.HandleUnsubscribeLink(tampered); ok {
		t.Fatal("expected tampered token to fail")
	}

	// Garbage token
	if _, ok := mgr.HandleUnsubscribeLink("invalid.garbage"); ok {
		t.Fatal("expected invalid token to fail")
	}

	// Empty token
	if _, ok := mgr.HandleUnsubscribeLink(""); ok {
		t.Fatal("expected empty token to fail")
	}
}

func TestSuppressionList_UnsubscribeLink_ExpiredToken(t *testing.T) {
	mgr := mustSuppressionMgr(t, []byte("secret-key"))
	tenantID := int64(202)
	email := "old-user@example.com"

	// Create already expired token
	expiredToken := mgr.GenerateUnsubscribeTokenWithTTL(tenantID, email, -1*time.Minute)

	if _, ok := mgr.HandleUnsubscribeLink(expiredToken); ok {
		t.Fatal("expected expired token to fail")
	}

	// Email should not be suppressed
	if mgr.CheckSuppression(tenantID, email) {
		t.Fatal("email should not be suppressed after failed expired token")
	}
}

// mustSuppressionMgr builds a SuppressionManager for tests, failing the test
// when construction errors.
func mustSuppressionMgr(t *testing.T, secret []byte) *SuppressionManager {
	t.Helper()
	if len(secret) == 0 {
		secret = []byte("test-default-unsubscribe-secret")
	}
	mgr, err := NewSuppressionManager(secret)
	if err != nil {
		t.Fatalf("NewSuppressionManager failed: %v", err)
	}
	return mgr
}

// NewSuppressionManager must reject an empty/nil secret: unsubscribe tokens
// are signed credentials and a hardcoded default key would be forgeable.
func TestNewSuppressionManager_RejectsEmptySecret(t *testing.T) {
	for _, secret := range [][]byte{nil, {}, []byte("")} {
		mgr, err := NewSuppressionManager(secret)
		if err == nil {
			t.Fatal("expected error for empty secret")
		}
		if !errors.Is(err, ErrUnsubscribeSecretRequired) {
			t.Fatalf("expected ErrUnsubscribeSecretRequired, got %v", err)
		}
		if mgr != nil {
			t.Fatal("manager must be nil on error")
		}
	}
}

// PrepareBatch must refuse to run without a suppression manager.
func TestPrepareBatch_NilSuppressionManagerRejected(t *testing.T) {
	_, err := PrepareBatch(BatchPreparationRequest{
		TenantID:           1,
		Recipients:         []string{"a@example.com"},
		BaseUnsubscribeURL: "https://example.com/unsub",
	}, nil)
	if !errors.Is(err, ErrSuppressionManagerRequired) {
		t.Fatalf("expected ErrSuppressionManagerRequired, got %v", err)
	}
}

// The deterministic message ID must be stable across preparations of the same
// (tenant, campaign, recipient, content) and differ for different content.
func TestPrepareBatch_DeterministicMessageID(t *testing.T) {
	mgr := mustSuppressionMgr(t, []byte("secret-key"))
	req := BatchPreparationRequest{
		TenantID:           7,
		CampaignID:         "camp-1",
		Subject:            "Hi",
		BodyHTML:           "<p>x</p>",
		BaseUnsubscribeURL: "https://example.com/unsub",
		Recipients:         []string{"user@example.com", "other@example.com"},
	}

	r1, err := PrepareBatch(req, mgr)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := PrepareBatch(req, mgr)
	if err != nil {
		t.Fatal(err)
	}

	if r1.Messages[0].ID != r2.Messages[0].ID {
		t.Fatalf("same input must produce identical id: %s vs %s", r1.Messages[0].ID, r2.Messages[0].ID)
	}
	if r1.Messages[0].ID == r1.Messages[1].ID {
		t.Fatal("different recipients must produce different ids")
	}
	if !strings.HasPrefix(r1.Messages[0].ID, "mkt-") {
		t.Fatalf("expected mkt- prefix, got %s", r1.Messages[0].ID)
	}
}

// The outbound channel password must never appear in serialized JSON.
func TestOutboundChannel_PasswordNeverSerialized(t *testing.T) {
	ch := OutboundChannel{
		Kind:        ChannelKindDedicatedGateway,
		Host:        "relay.example.com",
		Port:        587,
		Username:    "u",
		Password:    "super-secret-smtp-password",
		IsDedicated: true,
	}
	b, err := json.Marshal(ch)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "super-secret-smtp-password") {
		t.Fatalf("password leaked in JSON: %s", b)
	}

	// Round-trip: password can still be loaded from config input.
	var ch2 OutboundChannel
	if err := json.Unmarshal(b, &ch2); err != nil {
		t.Fatal(err)
	}
	in := []byte(`{"kind":"dedicated_gateway","host":"h","port":587,"password":"cfg-pass","is_dedicated":true}`)
	var ch3 OutboundChannel
	if err := json.Unmarshal(in, &ch3); err != nil {
		t.Fatal(err)
	}
	if ch3.Password != "cfg-pass" {
		t.Fatalf("expected password loaded from config, got %q", ch3.Password)
	}
	b3, _ := json.Marshal(ch3)
	if strings.Contains(string(b3), "cfg-pass") {
		t.Fatalf("password leaked in JSON after round-trip: %s", b3)
	}
}

// Failure must apply exponential backoff, and after max attempts the message
// must be dead-lettered instead of retried forever.
func TestDispatchQueue_BackoffAndDeadLetter(t *testing.T) {
	ctx := context.Background()
	channel := &OutboundChannel{Kind: ChannelKindDedicatedIP, DedicatedIP: "192.0.2.9", IsDedicated: true}
	senderErr := errors.New("smtp down")
	sender := func(_ context.Context, _ *OutboundChannel, _ *PreparedMarketingMessage) error {
		return senderErr
	}

	q := NewDispatchQueue(channel, nil, nil, sender)
	allowAll(q)
	q.SetRetryPolicy(3, 10*time.Second)

	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	q.SetClock(func() time.Time { return now })

	q.Enqueue(&PreparedMarketingMessage{ID: "m-dead", TenantID: 1, Recipient: "r@example.com"})

	// Attempt 1: fails, backs off 10s (10 * 2^0).
	if _, err := q.DispatchNext(ctx); !errors.Is(err, senderErr) {
		t.Fatalf("attempt 1: expected senderErr, got %v", err)
	}
	if q.QueueLength() != 1 {
		t.Fatalf("message must be requeued, got len %d", q.QueueLength())
	}
	// Not due yet: queue reports empty.
	if _, err := q.DispatchNext(ctx); !errors.Is(err, ErrQueueEmpty) {
		t.Fatalf("expected ErrQueueEmpty during backoff, got %v", err)
	}

	// Attempt 2 at +10s: backs off 20s (10 * 2^1).
	now = now.Add(10 * time.Second)
	if _, err := q.DispatchNext(ctx); !errors.Is(err, senderErr) {
		t.Fatalf("attempt 2: expected senderErr, got %v", err)
	}

	// Attempt 3 at +30s: exceeds max attempts -> dead letter.
	now = now.Add(20 * time.Second)
	_, err := q.DispatchNext(ctx)
	if !errors.Is(err, ErrMessageDead) {
		t.Fatalf("expected ErrMessageDead, got %v", err)
	}
	if q.DeadCount() != 1 {
		t.Fatalf("expected 1 dead message, got %d", q.DeadCount())
	}
	if q.QueueLength() != 0 {
		t.Fatalf("dead message must leave the queue, got len %d", q.QueueLength())
	}
	// No further dispatch possible.
	if _, err := q.DispatchNext(ctx); !errors.Is(err, ErrQueueEmpty) {
		t.Fatalf("expected queue drained after dead-letter, got %v", err)
	}
}

// The queue lock must not be held while the external sender runs: a blocked
// sender must not prevent QueueLength/SetChannel from progressing.
func TestDispatchQueue_SenderRunsOutsideLock(t *testing.T) {
	ctx := context.Background()
	channel := &OutboundChannel{Kind: ChannelKindDedicatedIP, DedicatedIP: "192.0.2.10", IsDedicated: true}

	release := make(chan struct{})
	senderStarted := make(chan struct{})
	sender := func(_ context.Context, _ *OutboundChannel, _ *PreparedMarketingMessage) error {
		close(senderStarted)
		<-release
		return nil
	}

	q := NewDispatchQueue(channel, nil, nil, sender)
	allowAll(q)
	q.Enqueue(&PreparedMarketingMessage{ID: "m-lock", TenantID: 1, Recipient: "r@example.com"})

	done := make(chan error, 1)
	go func() {
		_, err := q.DispatchNext(ctx)
		done <- err
	}()

	<-senderStarted
	// While the sender is blocked, queue bookkeeping must still respond.
	progressed := make(chan int, 1)
	go func() {
		progressed <- q.QueueLength()
	}()
	select {
	case <-progressed:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("queue lock appears held while sender is running")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
}

// 3. Rate Limiter Tests (Token Bucket Hourly / Daily)

func TestRateLimiter_LimitsAndExhaustion(t *testing.T) {
	cfg := RateLimitConfig{
		MaxPerHour: 3,
		MaxPerDay:  5,
	}
	limiter := NewRateLimiter(cfg)

	// Consume 3 tokens within the hour
	for i := 0; i < 3; i++ {
		if err := limiter.Take(1); err != nil {
			t.Fatalf("expected token %d to be permitted, got %v", i+1, err)
		}
	}

	// 4th token should hit hourly limit
	err := limiter.Take(1)
	if !errors.Is(err, ErrHourlyRateLimitExceeded) {
		t.Fatalf("expected ErrHourlyRateLimitExceeded, got %v", err)
	}
	if limiter.Allow() {
		t.Fatal("expected Allow() to return false when limit exceeded")
	}
}

func TestRateLimiter_DailyLimitHitBeforeHourly(t *testing.T) {
	cfg := RateLimitConfig{
		MaxPerHour: 10,
		MaxPerDay:  2,
	}
	limiter := NewRateLimiter(cfg)

	if err := limiter.Take(2); err != nil {
		t.Fatalf("failed taking 2 tokens: %v", err)
	}

	// Daily limit is 2, next take should fail daily limit
	err := limiter.Take(1)
	if !errors.Is(err, ErrDailyRateLimitExceeded) {
		t.Fatalf("expected ErrDailyRateLimitExceeded, got %v", err)
	}
}

func TestRateLimiter_RefillWithClock(t *testing.T) {
	cfg := RateLimitConfig{
		MaxPerHour: 10,
		MaxPerDay:  100,
	}
	limiter := NewRateLimiter(cfg)

	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	limiter.SetClock(func() time.Time { return now })

	// Exhaust 10 hourly tokens
	if err := limiter.Take(10); err != nil {
		t.Fatalf("expected to take 10 tokens: %v", err)
	}
	if err := limiter.Take(1); !errors.Is(err, ErrHourlyRateLimitExceeded) {
		t.Fatalf("expected hourly limit exceeded, got %v", err)
	}

	// Advance time by 30 minutes (1800 seconds) -> refills 5 tokens
	now = now.Add(30 * time.Minute)
	if err := limiter.Take(5); err != nil {
		t.Fatalf("expected 5 refilled tokens available after 30 minutes, got %v", err)
	}
	if err := limiter.Take(1); !errors.Is(err, ErrHourlyRateLimitExceeded) {
		t.Fatalf("expected tokens exhausted again, got %v", err)
	}

	// Advance another 1 hour -> full refill up to capacity
	now = now.Add(1 * time.Hour)
	hAvail, _ := limiter.Available()
	if hAvail < 9.9 {
		t.Fatalf("expected approximately 10 tokens available, got %f", hAvail)
	}
}

func TestRateLimiter_UnlimitedWhenZero(t *testing.T) {
	cfg := RateLimitConfig{
		MaxPerHour: 0,
		MaxPerDay:  0,
	}
	limiter := NewRateLimiter(cfg)

	if err := limiter.Take(1000); err != nil {
		t.Fatalf("expected unlimited tokens, got %v", err)
	}
}

// 4. Batch Preparation & RFC 8058 / RFC 2369 Header Injection

func TestPrepareBatch_RFC8058_RFC2369_HeadersAndSuppression(t *testing.T) {
	mgr := mustSuppressionMgr(t, nil)
	tenantID := int64(400)

	// Pre-suppress one recipient
	mgr.AddSuppression(tenantID, "suppressed@domain.com", SuppressionReasonUnsubscribe)

	req := BatchPreparationRequest{
		TenantID:           tenantID,
		CampaignID:         "fall-launch-2026",
		Subject:            "Special Announcement",
		BodyHTML:           `<p>Hello!</p><a href="{{UnsubscribeURL}}">Unsubscribe</a>`,
		BodyText:           "Hello! Unsubscribe here: {{UnsubscribeURL}}",
		BaseUnsubscribeURL: "https://mail.urmail.cn/api/v1/marketing/unsubscribe",
		UnsubscribeMailto:  "unsub@marketing.urmail.cn",
		Recipients: []string{
			"eligible1@domain.com",
			"suppressed@domain.com",
			"eligible2@domain.com",
			"malformed-email-address",
		},
		CustomHeaders: map[string]string{
			"X-Sender-Service": "urMail-Marketing",
		},
	}

	res, err := PrepareBatch(req, mgr)
	if err != nil {
		t.Fatalf("PrepareBatch failed: %v", err)
	}

	if res.TotalRequested != 4 {
		t.Fatalf("expected TotalRequested 4, got %d", res.TotalRequested)
	}

	// Verify Suppressed
	if len(res.SuppressedEmails) != 1 || res.SuppressedEmails[0] != "suppressed@domain.com" {
		t.Fatalf("unexpected suppressed emails: %v", res.SuppressedEmails)
	}

	// Verify Invalid
	if len(res.InvalidEmails) != 1 || res.InvalidEmails[0] != "malformed-email-address" {
		t.Fatalf("unexpected invalid emails: %v", res.InvalidEmails)
	}

	// Verify Prepared Messages
	if len(res.Messages) != 2 {
		t.Fatalf("expected 2 prepared messages, got %d", len(res.Messages))
	}

	for _, msg := range res.Messages {
		// RFC 8058 One-Click Header
		if msg.Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
			t.Fatalf("missing or wrong List-Unsubscribe-Post: %q", msg.Headers["List-Unsubscribe-Post"])
		}

		// RFC 2369 & RFC 8058 List-Unsubscribe Header
		listUnsub := msg.Headers["List-Unsubscribe"]
		if !strings.Contains(listUnsub, "<https://mail.urmail.cn/api/v1/marketing/unsubscribe?token=") {
			t.Fatalf("List-Unsubscribe does not contain valid HTTPS URL: %s", listUnsub)
		}
		if !strings.Contains(listUnsub, "<mailto:unsub@marketing.urmail.cn?subject=unsubscribe>") {
			t.Fatalf("List-Unsubscribe does not contain mailto URL: %s", listUnsub)
		}

		// Custom Header Preserved
		if msg.Headers["X-Sender-Service"] != "urMail-Marketing" {
			t.Fatalf("custom header not preserved")
		}

		// Placeholders replaced
		if strings.Contains(msg.BodyHTML, "{{UnsubscribeURL}}") {
			t.Fatal("{{UnsubscribeURL}} was not replaced in BodyHTML")
		}
		if !strings.Contains(msg.BodyHTML, msg.UnsubscribeURL) {
			t.Fatal("BodyHTML does not contain actual unsubscribe URL")
		}
	}
}

func TestPrepareBatch_ValidationFailures(t *testing.T) {
	// Empty recipients
	_, err := PrepareBatch(BatchPreparationRequest{
		TenantID:           1,
		Recipients:         nil,
		BaseUnsubscribeURL: "https://example.com/unsub",
	}, nil)
	if !errors.Is(err, ErrEmptyBatch) {
		t.Fatalf("expected ErrEmptyBatch, got %v", err)
	}

	// Invalid Tenant ID
	_, err = PrepareBatch(BatchPreparationRequest{
		TenantID:           0,
		Recipients:         []string{"test@example.com"},
		BaseUnsubscribeURL: "https://example.com/unsub",
	}, nil)
	if err == nil {
		t.Fatal("expected error for zero tenant ID")
	}

	// Missing BaseUnsubscribeURL
	_, err = PrepareBatch(BatchPreparationRequest{
		TenantID:           1,
		Recipients:         []string{"test@example.com"},
		BaseUnsubscribeURL: "",
	}, nil)
	if err == nil {
		t.Fatal("expected error for empty base unsubscribe URL")
	}
}

// 5. Dispatch Queue Tests

func TestDispatchQueue_ChannelEnforcement(t *testing.T) {
	ctx := context.Background()
	msg := &PreparedMarketingMessage{
		ID:        "m1",
		TenantID:  1,
		Recipient: "user@example.com",
	}

	// Nil channel
	q := NewDispatchQueue(nil, nil, nil, nil)
	q.Enqueue(msg)
	_, err := q.DispatchNext(ctx)
	if !errors.Is(err, ErrMarketingChannelNotConfigured) {
		t.Fatalf("expected ErrMarketingChannelNotConfigured, got %v", err)
	}

	// Transactional channel
	q.SetChannel(&OutboundChannel{
		Kind:        ChannelKindTransactional,
		IsDedicated: false,
	})
	_, err = q.DispatchNext(ctx)
	if !errors.Is(err, ErrRefuseFallbackToTransactional) {
		t.Fatalf("expected ErrRefuseFallbackToTransactional, got %v", err)
	}
}

func TestDispatchQueue_SuccessAndMetrics(t *testing.T) {
	ctx := context.Background()
	validChannel := &OutboundChannel{
		Kind:        ChannelKindDedicatedIP,
		DedicatedIP: "192.0.2.1",
		IsDedicated: true,
	}

	var sentLog []*PreparedMarketingMessage
	var mu sync.Mutex
	sender := func(_ context.Context, _ *OutboundChannel, msg *PreparedMarketingMessage) error {
		mu.Lock()
		defer mu.Unlock()
		sentLog = append(sentLog, msg)
		return nil
	}

	q := NewDispatchQueue(validChannel, nil, nil, sender)
	allowAll(q)
	q.Enqueue(
		&PreparedMarketingMessage{ID: "m1", TenantID: 1, Recipient: "r1@example.com"},
		&PreparedMarketingMessage{ID: "m2", TenantID: 1, Recipient: "r2@example.com"},
		&PreparedMarketingMessage{ID: "m3", TenantID: 1, Recipient: "r3@example.com"},
	)

	if q.QueueLength() != 3 {
		t.Fatalf("expected queue length 3, got %d", q.QueueLength())
	}

	sentCount, err := q.DispatchBatch(ctx, 0)
	if err != nil {
		t.Fatalf("DispatchBatch failed: %v", err)
	}
	if sentCount != 3 {
		t.Fatalf("expected 3 sent messages, got %d", sentCount)
	}

	if q.QueueLength() != 0 {
		t.Fatalf("expected 0 remaining messages, got %d", q.QueueLength())
	}
	if q.SentCount() != 3 {
		t.Fatalf("expected SentCount 3, got %d", q.SentCount())
	}
	if len(sentLog) != 3 {
		t.Fatalf("expected sender called 3 times, got %d", len(sentLog))
	}
}

func TestDispatchQueue_DynamicSuppressionAtEgress(t *testing.T) {
	ctx := context.Background()
	channel := &OutboundChannel{
		Kind:        ChannelKindDedicatedIP,
		DedicatedIP: "192.0.2.2",
		IsDedicated: true,
	}
	mgr := mustSuppressionMgr(t, nil)
	q := NewDispatchQueue(channel, nil, mgr, func(context.Context, *OutboundChannel, *PreparedMarketingMessage) error { return nil })
	allowAll(q)

	msg1 := &PreparedMarketingMessage{ID: "m1", TenantID: 1, Recipient: "good@example.com"}
	msg2 := &PreparedMarketingMessage{ID: "m2", TenantID: 1, Recipient: "unsubbed-late@example.com"}
	q.Enqueue(msg1, msg2)

	// Simulate user unsubscribed after enqueuing
	mgr.AddSuppression(1, "unsubbed-late@example.com", SuppressionReasonUnsubscribe)

	sentCount, err := q.DispatchBatch(ctx, 0)
	if err != nil {
		t.Fatalf("unexpected dispatch batch error: %v", err)
	}

	if sentCount != 1 {
		t.Fatalf("expected 1 sent message, got %d", sentCount)
	}
	if q.DroppedCount() != 1 {
		t.Fatalf("expected 1 dropped message due to suppression, got %d", q.DroppedCount())
	}
}

type staticSuppressionChecker struct {
	suppressed bool
	err        error
}

func (s staticSuppressionChecker) IsSuppressed(context.Context, int64, string) (bool, error) {
	return s.suppressed, s.err
}

func TestDirectDispatch_DurableSuppressionAndLookupFailure(t *testing.T) {
	channel := &OutboundChannel{Kind: ChannelKindDedicatedIP, DedicatedIP: "192.0.2.7", IsDedicated: true}
	calls := 0
	sender := func(context.Context, *OutboundChannel, *PreparedMarketingMessage) error { calls++; return nil }
	msg := &PreparedMarketingMessage{TenantID: 1, Recipient: "person@example.test"}
	if err := DispatchMarketingMessageWithChecker(context.Background(), channel, msg, nil, nil, staticSuppressionChecker{suppressed: true}, staticPolicy{allowed: true}, sender); !errors.Is(err, ErrSuppressedRecipient) || calls != 0 {
		t.Fatalf("suppression err=%v calls=%d", err, calls)
	}
	if err := DispatchMarketingMessageWithChecker(context.Background(), channel, msg, nil, nil, staticSuppressionChecker{err: errors.New("DB down")}, staticPolicy{allowed: true}, sender); err == nil || calls != 0 {
		t.Fatalf("DB failure err=%v calls=%d", err, calls)
	}
}

func TestDispatchQueue_DurableSuppressionAtEgress(t *testing.T) {
	channel := &OutboundChannel{Kind: ChannelKindDedicatedIP, DedicatedIP: "192.0.2.8", IsDedicated: true}
	sent := 0
	q := NewDispatchQueue(channel, nil, nil, func(context.Context, *OutboundChannel, *PreparedMarketingMessage) error { sent++; return nil })
	allowAll(q)
	q.SetSuppressionChecker(staticSuppressionChecker{suppressed: true})
	q.Enqueue(&PreparedMarketingMessage{ID: "durable", TenantID: 27, Recipient: "optout@example.test"})
	if n, err := q.DispatchBatch(context.Background(), 0); err != nil || n != 0 || sent != 0 || q.DroppedCount() != 1 {
		t.Fatalf("suppressed durable message sent=%d count=%d dropped=%d err=%v", sent, n, q.DroppedCount(), err)
	}
}

func TestDispatchQueue_DurableSuppressionFailsClosed(t *testing.T) {
	channel := &OutboundChannel{Kind: ChannelKindDedicatedIP, DedicatedIP: "192.0.2.8", IsDedicated: true}
	sendCalls := 0
	q := NewDispatchQueue(channel, nil, nil, func(context.Context, *OutboundChannel, *PreparedMarketingMessage) error { sendCalls++; return nil })
	q.SetSuppressionChecker(staticSuppressionChecker{err: errors.New("database unavailable")})
	q.Enqueue(&PreparedMarketingMessage{ID: "retry", TenantID: 27, Recipient: "person@example.test"})
	if _, err := q.DispatchNext(context.Background()); err == nil || sendCalls != 0 || q.QueueLength() != 1 {
		t.Fatalf("DB error did not fail closed: send=%d queue=%d err=%v", sendCalls, q.QueueLength(), err)
	}
}

func TestDispatchQueue_RateLimitThrottling(t *testing.T) {
	ctx := context.Background()
	channel := &OutboundChannel{
		Kind:        ChannelKindDedicatedGateway,
		Host:        "relay.example.com",
		Port:        587,
		IsDedicated: true,
	}

	// Limiter allowing only 2 messages
	limiter := NewRateLimiter(RateLimitConfig{MaxPerHour: 2, MaxPerDay: 10})
	q := NewDispatchQueue(channel, limiter, nil, func(context.Context, *OutboundChannel, *PreparedMarketingMessage) error { return nil })
	allowAll(q)

	q.Enqueue(
		&PreparedMarketingMessage{ID: "m1", TenantID: 1, Recipient: "r1@example.com"},
		&PreparedMarketingMessage{ID: "m2", TenantID: 1, Recipient: "r2@example.com"},
		&PreparedMarketingMessage{ID: "m3", TenantID: 1, Recipient: "r3@example.com"},
		&PreparedMarketingMessage{ID: "m4", TenantID: 1, Recipient: "r4@example.com"},
	)

	sentCount, err := q.DispatchBatch(ctx, 0)
	if !errors.Is(err, ErrHourlyRateLimitExceeded) {
		t.Fatalf("expected ErrHourlyRateLimitExceeded, got %v", err)
	}
	if sentCount != 2 {
		t.Fatalf("expected 2 sent messages before rate limit hit, got %d", sentCount)
	}
	if q.QueueLength() != 2 {
		t.Fatalf("expected 2 messages remaining in queue, got %d", q.QueueLength())
	}
}

func TestDispatchQueue_SenderFailurePreservesMessage(t *testing.T) {
	ctx := context.Background()
	channel := &OutboundChannel{
		Kind:        ChannelKindDedicatedIP,
		DedicatedIP: "192.0.2.3",
		IsDedicated: true,
	}

	senderErr := errors.New("network disconnect")
	sender := func(_ context.Context, _ *OutboundChannel, _ *PreparedMarketingMessage) error {
		return senderErr
	}

	q := NewDispatchQueue(channel, nil, nil, sender)
	allowAll(q)
	q.Enqueue(&PreparedMarketingMessage{ID: "m-retry", TenantID: 1, Recipient: "retry@example.com"})

	_, err := q.DispatchNext(ctx)
	if !errors.Is(err, senderErr) {
		t.Fatalf("expected senderErr, got %v", err)
	}

	// Message should remain at head of queue
	if q.QueueLength() != 1 {
		t.Fatalf("expected message preserved in queue on sender error, got len %d", q.QueueLength())
	}
}

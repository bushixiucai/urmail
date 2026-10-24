package mail

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ChannelKind represents marketing outbound channel kinds.
type ChannelKind string

const (
	// ChannelKindDedicatedIP uses a dedicated host IP bound for egress.
	ChannelKindDedicatedIP ChannelKind = "dedicated_ip"
	// ChannelKindDedicatedGateway uses an independent SMTP relay/gateway server.
	ChannelKindDedicatedGateway ChannelKind = "dedicated_gateway"
	// ChannelKindTransactional is the shared transactional channel (refused for marketing).
	ChannelKindTransactional ChannelKind = "transactional"
)

// Suppression reasons.
const (
	SuppressionReasonUnsubscribe = "unsubscribe"
	SuppressionReasonBounce      = "bounce"
	SuppressionReasonComplaint   = "complaint"
	SuppressionReasonManual      = "manual"
)

// Domain errors for marketing dispatch, channels, and suppression.
var (
	// ErrMarketingChannelNotConfigured indicates missing or invalid independent marketing channel configuration.
	ErrMarketingChannelNotConfigured = errors.New("marketing outbound channel not configured")

	// ErrRefuseFallbackToTransactional enforces P08: marketing mail must never fall back to transactional channel.
	ErrRefuseFallbackToTransactional = errors.New("refusing fallback to transactional channel for marketing email")

	// ErrSuppressedRecipient indicates the recipient is on the suppression list.
	ErrSuppressedRecipient = errors.New("recipient email is suppressed")

	// ErrHourlyRateLimitExceeded indicates the campaign/tenant has exceeded hourly token quota.
	ErrHourlyRateLimitExceeded = errors.New("hourly marketing rate limit exceeded")

	// ErrDailyRateLimitExceeded indicates the campaign/tenant has exceeded daily token quota.
	ErrDailyRateLimitExceeded = errors.New("daily marketing rate limit exceeded")

	// ErrInvalidUnsubscribeToken indicates an unrecognized, tampered, or expired unsubscribe token.
	ErrInvalidUnsubscribeToken = errors.New("invalid or expired unsubscribe token")

	// ErrEmptyBatch indicates a batch preparation request with no recipients.
	ErrEmptyBatch = errors.New("batch recipients list is empty")

	// ErrInvalidRecipient indicates a malformed recipient email address.
	ErrInvalidRecipient = errors.New("invalid recipient email address")

	// ErrQueueEmpty indicates the dispatch queue has no messages remaining.
	ErrQueueEmpty = errors.New("dispatch queue is empty")

	// ErrUnsubscribeSecretRequired indicates NewSuppressionManager was called
	// without an injected unsubscribe token secret. There is deliberately no
	// default secret: tokens are anti-abuse credentials and a hardcoded key
	// would let anyone forge unsubscribe links.
	ErrUnsubscribeSecretRequired = errors.New("unsubscribe token secret must be injected and must not be empty")

	// ErrSuppressionManagerRequired indicates PrepareBatch was called without a
	// SuppressionManager; sending marketing mail without suppression checks is
	// not permitted.
	ErrSuppressionManagerRequired = errors.New("suppression manager must be provided")

	// ErrMessageDead indicates a message exceeded its maximum send attempts.
	ErrMessageDead = errors.New("marketing message exceeded max attempts and was marked dead")

	// ErrSuppressionCheckerRequired indicates a dispatch queue that sends mail
	// was started without a durable SuppressionChecker. Fail closed: a queue
	// that cannot consult the durable suppression list must not send.
	ErrSuppressionCheckerRequired = errors.New("durable suppression checker must be installed before dispatching marketing mail")

	// ErrSenderPolicyRequired indicates no SenderPolicy is installed. Fail
	// closed: tenant allowance must be verified before every marketing send.
	ErrSenderPolicyRequired = errors.New("sender policy must be installed before dispatching marketing mail")

	// ErrSenderRequired rejects missing physical senders instead of reporting a false success.
	ErrSenderRequired = errors.New("physical marketing sender must be installed")

	// ErrSenderPolicyDenied indicates the tenant policy refused the send
	// (for example urmail_mail_allowed(tenant) is false, or the daily tenant
	// quota is exhausted). The message is skipped and recorded, not retried.
	ErrSenderPolicyDenied = errors.New("tenant is not allowed to send marketing mail")

	// ErrDeliveryUncertain indicates a send attempt failed with an unknown
	// delivery outcome (timeout after DATA, connection drop, ...). The message
	// is recorded as uncertain and is never silently re-sent.
	ErrDeliveryUncertain = errors.New("marketing delivery outcome is uncertain")

	// ErrInvalidCustomHeader indicates a custom header name or value contains
	// characters that would allow header injection.
	ErrInvalidCustomHeader = errors.New("invalid custom header")
)

// UncertainSendError marks a sender failure whose delivery outcome is unknown:
// the message may or may not have been handed to the remote MTA. Senders must
// wrap such errors with MarkDeliveryUncertain; the dispatch queue records the
// message as uncertain instead of re-queueing it for another send.
type UncertainSendError struct{ Err error }

func (e *UncertainSendError) Error() string {
	if e.Err == nil {
		return ErrDeliveryUncertain.Error()
	}
	return ErrDeliveryUncertain.Error() + ": " + e.Err.Error()
}

func (e *UncertainSendError) Unwrap() error { return e.Err }

// MarkDeliveryUncertain wraps err so the dispatch queue treats the attempt as
// having an unknown outcome. A nil error is returned unchanged.
func MarkDeliveryUncertain(err error) error {
	if err == nil {
		return nil
	}
	return &UncertainSendError{Err: err}
}

// IsDeliveryUncertain reports whether err was marked as an uncertain delivery.
func IsDeliveryUncertain(err error) bool {
	var u *UncertainSendError
	return errors.As(err, &u)
}

// SenderPolicy is the pluggable SMTP-policy hook consulted before every
// marketing send. The implementation lives outside the domain package (the
// parent wires it to urmail_mail_allowed(tenant) and the tenant daily send
// limit); the domain package only defines the contract.
type SenderPolicy interface {
	// AllowedToSend reports whether the tenant may send marketing mail now.
	// An error must be treated as a refusal (fail closed).
	AllowedToSend(tenantID int64) (bool, error)
}

// OutboundChannel represents an egress configuration for dispatching mail.
// P08 requires marketing campaigns to use an independent outbound channel
// (dedicated IP or dedicated gateway) and strictly forbids fallback to transactional channel.
type OutboundChannel struct {
	Kind        ChannelKind `json:"kind"`
	Host        string      `json:"host,omitempty"`
	Port        int         `json:"port,omitempty"`
	DedicatedIP string      `json:"dedicated_ip,omitempty"`
	Username    string      `json:"username,omitempty"`
	// Password is sensitive: it is never serialized (see MarshalJSON below),
	// but is accepted when unmarshaling configuration.
	Password    string `json:"-"`
	IsDedicated bool   `json:"is_dedicated"`
}

// MarshalJSON serializes the channel without ever emitting the SMTP password.
// A dedicated `json:"-"` tag plus explicit marshal keeps secrets out of logs,
// API responses, and audit dumps even when the struct is embedded elsewhere.
func (ch OutboundChannel) MarshalJSON() ([]byte, error) {
	type plain OutboundChannel
	p := plain(ch)
	p.Password = ""
	return json.Marshal(p)
}

// UnmarshalJSON accepts the password field on input for configuration loading.
func (ch *OutboundChannel) UnmarshalJSON(data []byte) error {
	var p struct {
		Kind        ChannelKind `json:"kind"`
		Host        string      `json:"host"`
		Port        int         `json:"port"`
		DedicatedIP string      `json:"dedicated_ip"`
		Username    string      `json:"username"`
		Password    string      `json:"password"`
		IsDedicated bool        `json:"is_dedicated"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	ch.Kind = p.Kind
	ch.Host = p.Host
	ch.Port = p.Port
	ch.DedicatedIP = p.DedicatedIP
	ch.Username = p.Username
	ch.Password = p.Password
	ch.IsDedicated = p.IsDedicated
	return nil
}

// ValidateMarketingChannel validates that the outbound channel meets P08 requirements.
// Marketing dispatch MUST fail immediately if no independent channel is configured,
// and MUST refuse to fall back to the transactional mail channel.
func ValidateMarketingChannel(ch *OutboundChannel) error {
	if ch == nil {
		return ErrMarketingChannelNotConfigured
	}

	// Strictly refuse transactional channel or channels not explicitly marked dedicated.
	if ch.Kind == ChannelKindTransactional || !ch.IsDedicated {
		return ErrRefuseFallbackToTransactional
	}

	switch ch.Kind {
	case ChannelKindDedicatedIP:
		ip := strings.TrimSpace(ch.DedicatedIP)
		if ip == "" {
			return fmt.Errorf("%w: dedicated IP address missing", ErrMarketingChannelNotConfigured)
		}
		return nil

	case ChannelKindDedicatedGateway:
		host := strings.TrimSpace(ch.Host)
		if host == "" || ch.Port <= 0 {
			return fmt.Errorf("%w: dedicated gateway host or port missing", ErrMarketingChannelNotConfigured)
		}
		return nil

	default:
		return fmt.Errorf("%w: unsupported channel kind %q", ErrMarketingChannelNotConfigured, ch.Kind)
	}
}

// SuppressionEntry records a suppressed recipient for a specific tenant.
type SuppressionEntry struct {
	TenantID  int64     `json:"tenant_id"`
	Email     string    `json:"email"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

type unsubscribeTokenPayload struct {
	TenantID  int64  `json:"t"`
	Email     string `json:"e"`
	ExpiresAt int64  `json:"exp"`
}

// SuppressionManager handles tenant suppression lists and unsubscribe tokens.
type SuppressionManager struct {
	mu      sync.RWMutex
	entries map[string]SuppressionEntry        // key: fmt.Sprintf("%d:%s", tenantID, normalizedEmail)
	tokens  map[string]unsubscribeTokenPayload // fast lookup for in-memory tokens
	secret  []byte
}

// NewSuppressionManager creates an isolated SuppressionManager instance.
// The unsubscribe token secret MUST be injected by the caller (Config/env);
// an empty secret is rejected with ErrUnsubscribeSecretRequired.
func NewSuppressionManager(secret []byte) (*SuppressionManager, error) {
	if len(secret) == 0 {
		return nil, ErrUnsubscribeSecretRequired
	}
	secCopy := make([]byte, len(secret))
	copy(secCopy, secret)

	return &SuppressionManager{
		entries: make(map[string]SuppressionEntry),
		tokens:  make(map[string]unsubscribeTokenPayload),
		secret:  secCopy,
	}, nil
}

// AddSuppression adds an email address to the tenant suppression list.
func (sm *SuppressionManager) AddSuppression(tenantID int64, email, reason string) {
	norm := strings.ToLower(strings.TrimSpace(email))
	if norm == "" {
		return
	}
	if reason == "" {
		reason = SuppressionReasonManual
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	k := fmt.Sprintf("%d:%s", tenantID, norm)
	if current, exists := sm.entries[k]; exists && (current.Reason == SuppressionReasonUnsubscribe || current.Reason == SuppressionReasonBounce || current.Reason == SuppressionReasonComplaint) && reason != current.Reason {
		return
	}
	sm.entries[k] = SuppressionEntry{
		TenantID:  tenantID,
		Email:     norm,
		Reason:    reason,
		CreatedAt: time.Now().UTC(),
	}
}

// CheckSuppression returns true if the email is suppressed for the tenant.
func (sm *SuppressionManager) CheckSuppression(tenantID int64, email string) bool {
	norm := strings.ToLower(strings.TrimSpace(email))
	if norm == "" {
		return false
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	k := fmt.Sprintf("%d:%s", tenantID, norm)
	_, found := sm.entries[k]
	return found
}

// RemoveSuppression removes a manually added suppression; unsubscribe, bounce, and complaint records are retained.
func (sm *SuppressionManager) RemoveSuppression(tenantID int64, email string) bool {
	norm := strings.ToLower(strings.TrimSpace(email))
	if norm == "" {
		return false
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	k := fmt.Sprintf("%d:%s", tenantID, norm)
	if entry, found := sm.entries[k]; found {
		if entry.Reason != SuppressionReasonManual {
			return false
		}
		delete(sm.entries, k)
		return true
	}
	return false
}

// GenerateUnsubscribeToken produces a signed unsubscribe token valid for 90 days.
func (sm *SuppressionManager) GenerateUnsubscribeToken(tenantID int64, email string) string {
	return sm.GenerateUnsubscribeTokenWithTTL(tenantID, email, 90*24*time.Hour)
}

// GenerateUnsubscribeTokenWithTTL produces a signed unsubscribe token with custom TTL.
func (sm *SuppressionManager) GenerateUnsubscribeTokenWithTTL(tenantID int64, email string, ttl time.Duration) string {
	norm := strings.ToLower(strings.TrimSpace(email))
	exp := time.Now().UTC().Add(ttl).Unix()
	payload := unsubscribeTokenPayload{
		TenantID:  tenantID,
		Email:     norm,
		ExpiresAt: exp,
	}

	raw, _ := json.Marshal(payload)
	payloadB64 := base64.RawURLEncoding.EncodeToString(raw)

	h := hmac.New(sha256.New, sm.secret)
	h.Write([]byte(payloadB64))
	sigHex := hex.EncodeToString(h.Sum(nil))

	token := fmt.Sprintf("%s.%s", payloadB64, sigHex)

	sm.mu.Lock()
	sm.tokens[token] = payload
	sm.mu.Unlock()

	return token
}

// VerifyUnsubscribeToken validates a signed token without changing storage.
// It intentionally performs stateless verification so a token remains valid
// across process restarts.
func (sm *SuppressionManager) VerifyUnsubscribeToken(token string) (int64, string, bool) {
	// Nil-safe guard: a zero-value manager has no signing secret; verification
	// must fail closed instead of dereferencing sm.secret.
	if sm == nil {
		return 0, "", false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return 0, "", false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return 0, "", false
	}
	payloadB64, sigHex := parts[0], parts[1]
	h := hmac.New(sha256.New, sm.secret)
	_, _ = h.Write([]byte(payloadB64))
	expectedSig := hex.EncodeToString(h.Sum(nil))
	if !hmac.Equal([]byte(sigHex), []byte(expectedSig)) {
		return 0, "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return 0, "", false
	}
	var payload unsubscribeTokenPayload
	if err := json.Unmarshal(raw, &payload); err != nil || payload.TenantID <= 0 || payload.ExpiresAt <= time.Now().UTC().Unix() {
		return 0, "", false
	}
	email := strings.ToLower(strings.TrimSpace(payload.Email))
	if !strings.Contains(email, "@") || strings.ContainsAny(email, "\r\n <>") {
		return 0, "", false
	}
	return payload.TenantID, email, true
}

// HandleUnsubscribeLink validates the token and adds it to the in-memory list.
// Use VerifyUnsubscribeToken with a persistent store for production endpoints.
func (sm *SuppressionManager) HandleUnsubscribeLink(token string) (string, bool) {
	tenantID, email, ok := sm.VerifyUnsubscribeToken(token)
	if !ok {
		return "", false
	}
	sm.AddSuppression(tenantID, email, SuppressionReasonUnsubscribe)
	return email, true
}

// RateLimitConfig defines messages allowed per hour and per day.
type RateLimitConfig struct {
	MaxPerHour int
	MaxPerDay  int
}

// limiterBucket is one tenant's dual token bucket state.
type limiterBucket struct {
	hourlyTokens float64
	dailyTokens  float64
	lastRefill   time.Time
}

// RateLimiter implements per-tenant dual token buckets for hourly and daily
// marketing quotas. Each tenant gets an independent bucket (tenant 0 is the
// bucket used by the tenant-less convenience methods Take/Allow/Available).
// Buckets live in process memory: they refill over time but restart full, so
// durable quota enforcement belongs to the SenderPolicy / store layer.
type RateLimiter struct {
	mu      sync.Mutex
	cfg     RateLimitConfig
	buckets map[int64]*limiterBucket
	clock   func() time.Time
}

// NewRateLimiter creates a RateLimiter initialized with maximum token capacity.
func NewRateLimiter(cfg RateLimitConfig) *RateLimiter {
	now := time.Now().UTC()
	r := &RateLimiter{
		cfg:     cfg,
		buckets: make(map[int64]*limiterBucket),
		clock:   func() time.Time { return time.Now().UTC() },
	}
	r.buckets[0] = r.newBucket(now)
	return r
}

func (r *RateLimiter) newBucket(now time.Time) *limiterBucket {
	return &limiterBucket{
		hourlyTokens: float64(r.cfg.MaxPerHour),
		dailyTokens:  float64(r.cfg.MaxPerDay),
		lastRefill:   now,
	}
}

func (r *RateLimiter) bucketFor(tenantID int64, now time.Time) *limiterBucket {
	b, ok := r.buckets[tenantID]
	if !ok {
		b = r.newBucket(now)
		r.buckets[tenantID] = b
	}
	return b
}

// SetClock sets a custom clock function for deterministic time-travel testing.
func (r *RateLimiter) SetClock(fn func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clock = fn
	now := fn()
	// Re-baseline every existing bucket so no spurious refill is granted.
	for _, b := range r.buckets {
		b.lastRefill = now
	}
}

func refillBucket(cfg RateLimitConfig, b *limiterBucket, now time.Time) {
	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed <= 0 {
		return
	}

	if cfg.MaxPerHour > 0 {
		rate := float64(cfg.MaxPerHour) / 3600.0
		b.hourlyTokens = math.Min(float64(cfg.MaxPerHour), b.hourlyTokens+elapsed*rate)
	}

	if cfg.MaxPerDay > 0 {
		rate := float64(cfg.MaxPerDay) / 86400.0
		b.dailyTokens = math.Min(float64(cfg.MaxPerDay), b.dailyTokens+elapsed*rate)
	}

	b.lastRefill = now
}

// Allow attempts to consume 1 token from the default (tenant 0) bucket.
func (r *RateLimiter) Allow() bool {
	return r.Take(1) == nil
}

// Take attempts to consume n tokens from the default (tenant 0) bucket.
func (r *RateLimiter) Take(n int) error {
	return r.TakeFor(0, n)
}

// TakeFor attempts to consume n tokens from the tenant's own bucket.
func (r *RateLimiter) TakeFor(tenantID int64, n int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.takeAtFor(tenantID, r.clock(), n)
}

// TakeAt attempts to consume n tokens at the specified timestamp (tenant 0).
func (r *RateLimiter) TakeAt(now time.Time, n int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.takeAtFor(0, now, n)
}

func (r *RateLimiter) takeAtFor(tenantID int64, now time.Time, n int) error {
	if n <= 0 {
		return nil
	}

	b := r.bucketFor(tenantID, now)
	refillBucket(r.cfg, b, now)

	req := float64(n)
	if r.cfg.MaxPerHour > 0 && b.hourlyTokens < req {
		return ErrHourlyRateLimitExceeded
	}
	if r.cfg.MaxPerDay > 0 && b.dailyTokens < req {
		return ErrDailyRateLimitExceeded
	}

	if r.cfg.MaxPerHour > 0 {
		b.hourlyTokens -= req
	}
	if r.cfg.MaxPerDay > 0 {
		b.dailyTokens -= req
	}

	return nil
}

// RefundFor returns n previously consumed tokens to the tenant's bucket, used
// when a token was reserved but the send definitively failed. Refunds never
// exceed the bucket capacity.
func (r *RateLimiter) RefundFor(tenantID int64, n int) {
	if n <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock()
	b := r.bucketFor(tenantID, now)
	if r.cfg.MaxPerHour > 0 {
		b.hourlyTokens = math.Min(float64(r.cfg.MaxPerHour), b.hourlyTokens+float64(n))
	}
	if r.cfg.MaxPerDay > 0 {
		b.dailyTokens = math.Min(float64(r.cfg.MaxPerDay), b.dailyTokens+float64(n))
	}
}

// Available returns currently available hourly and daily tokens in the
// default (tenant 0) bucket.
func (r *RateLimiter) Available() (float64, float64) {
	return r.AvailableFor(0)
}

// AvailableFor returns currently available hourly and daily tokens for a tenant.
func (r *RateLimiter) AvailableFor(tenantID int64) (float64, float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.bucketFor(tenantID, r.clock())
	refillBucket(r.cfg, b, r.clock())
	return b.hourlyTokens, b.dailyTokens
}

// BatchPreparationRequest contains campaign payload and target recipients.
type BatchPreparationRequest struct {
	TenantID           int64             `json:"tenant_id"`
	CampaignID         string            `json:"campaign_id"`
	Subject            string            `json:"subject"`
	BodyHTML           string            `json:"body_html"`
	BodyText           string            `json:"body_text"`
	Recipients         []string          `json:"recipients"`
	BaseUnsubscribeURL string            `json:"base_unsubscribe_url"`
	UnsubscribeMailto  string            `json:"unsubscribe_mailto,omitempty"`
	CustomHeaders      map[string]string `json:"custom_headers,omitempty"`
}

// PreparedMarketingMessage is a validated, header-injected message ready for queue/egress.
type PreparedMarketingMessage struct {
	// ID is a deterministic idempotency key derived from tenant, campaign,
	// recipient, and content — the same (campaign, recipient, body) always
	// maps to the same ID, so retries never double-send.
	ID               string            `json:"id"`
	TenantID         int64             `json:"tenant_id"`
	CampaignID       string            `json:"campaign_id"`
	Recipient        string            `json:"recipient"`
	Subject          string            `json:"subject"`
	BodyHTML         string            `json:"body_html"`
	BodyText         string            `json:"body_text"`
	Headers          map[string]string `json:"headers"`
	UnsubscribeToken string            `json:"unsubscribe_token"`
	UnsubscribeURL   string            `json:"unsubscribe_url"`
	// Retry bookkeeping for DispatchQueue.
	Attempts    int       `json:"attempts"`
	NextRetryAt time.Time `json:"next_retry_at,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
}

// marketingMessageID derives a deterministic content+recipient idempotency key.
func marketingMessageID(tenantID int64, campaignID, recipient, subject, bodyHTML, bodyText string) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%d\x00%s\x00%s\x00%s\x00%s\x00%s", tenantID, campaignID, recipient, subject, bodyHTML, bodyText)
	return "mkt-" + hex.EncodeToString(h.Sum(nil))
}

// Default retry policy for DispatchQueue.
const (
	defaultMaxSendAttempts = 5
	defaultRetryBaseDelay  = 30 * time.Second
)

// retryBackoff returns exponential backoff for the given failed attempt count
// (1-indexed): base * 2^(attempt-1), capped to avoid overflow.
func retryBackoff(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = defaultRetryBaseDelay
	}
	d := base
	for i := 1; i < attempt && d < time.Hour; i++ {
		d *= 2
	}
	if d > time.Hour {
		d = time.Hour
	}
	return d
}

// BatchPreparationResult is the output of PrepareBatch.
type BatchPreparationResult struct {
	TenantID         int64                       `json:"tenant_id"`
	CampaignID       string                      `json:"campaign_id"`
	Messages         []*PreparedMarketingMessage `json:"messages"`
	SuppressedEmails []string                    `json:"suppressed_emails"`
	InvalidEmails    []string                    `json:"invalid_emails"`
	TotalRequested   int                         `json:"total_requested"`
}

// hasControlChars reports whether s contains CR, LF, NUL, or any other ASCII
// control character (including DEL). Such characters enable header/SMTP
// injection and are never acceptable in recipients or header values.
func hasControlChars(s string) bool {
	for _, c := range s {
		if c < 0x20 || c == 0x7F {
			return true
		}
	}
	return false
}

// validHeaderName enforces a conservative RFC 5322 token pattern for custom
// header names: printable ASCII letters, digits, and hyphens only.
func validHeaderName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, c := range name {
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// validRecipientAddress performs a stricter structural check than a bare
// "contains @": printable, single @, bounded local part, dot-separated domain
// labels, and an alphabetic TLD.
func validRecipientAddress(norm string) bool {
	if norm == "" || len(norm) > 254 || hasControlChars(norm) {
		return false
	}
	for _, c := range norm {
		if c < 0x21 || c > 0x7E {
			return false
		}
	}
	parts := strings.Split(norm, "@")
	if len(parts) != 2 {
		return false
	}
	local, domain := parts[0], parts[1]
	if local == "" || len(local) > 64 || strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return false
	}
	atextExtra := "!#$%&'*+/=?^_`{|}~.-"
	for _, c := range local {
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') &&
			!strings.ContainsRune(atextExtra, c) {
			return false
		}
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	for i, label := range labels {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
		if i == len(labels)-1 {
			// TLD must be alphabetic and at least two characters.
			if len(label) < 2 {
				return false
			}
			for _, c := range label {
				if c < 'a' || c > 'z' {
					return false
				}
			}
		}
	}
	return true
}

func buildUnsubscribeURL(baseURL, token string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		if strings.Contains(baseURL, "?") {
			return fmt.Sprintf("%s&token=%s", baseURL, url.QueryEscape(token))
		}
		return fmt.Sprintf("%s?token=%s", baseURL, url.QueryEscape(token))
	}
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()
	return u.String()
}

// PrepareBatch prepares marketing messages from recipients with suppression filtering
// and RFC 8058 & RFC 2369 List-Unsubscribe header injection.
// The suppression manager must be provided; nil is rejected.
func PrepareBatch(req BatchPreparationRequest, suppression *SuppressionManager) (*BatchPreparationResult, error) {
	if len(req.Recipients) == 0 {
		return nil, ErrEmptyBatch
	}
	if req.TenantID <= 0 {
		return nil, errors.New("invalid tenant ID")
	}
	if strings.TrimSpace(req.BaseUnsubscribeURL) == "" {
		return nil, errors.New("base unsubscribe URL is required")
	}
	if suppression == nil {
		return nil, ErrSuppressionManagerRequired
	}

	result := &BatchPreparationResult{
		TenantID:       req.TenantID,
		CampaignID:     req.CampaignID,
		TotalRequested: len(req.Recipients),
	}

	// Custom headers are validated once up front: names against a conservative
	// token pattern and values against control characters, so a bad header can
	// never produce a header-injection message.
	for k, v := range req.CustomHeaders {
		if !validHeaderName(k) || hasControlChars(v) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidCustomHeader, k)
		}
	}

	for _, r := range req.Recipients {
		norm := strings.ToLower(strings.TrimSpace(r))
		if !validRecipientAddress(norm) {
			result.InvalidEmails = append(result.InvalidEmails, r)
			continue
		}

		if suppression.CheckSuppression(req.TenantID, norm) {
			result.SuppressedEmails = append(result.SuppressedEmails, norm)
			continue
		}

		token := suppression.GenerateUnsubscribeToken(req.TenantID, norm)
		unsubURL := buildUnsubscribeURL(req.BaseUnsubscribeURL, token)

		// RFC 2369 & RFC 8058 List-Unsubscribe headers
		var listUnsubscribeVal string
		if req.UnsubscribeMailto != "" {
			listUnsubscribeVal = fmt.Sprintf("<mailto:%s?subject=unsubscribe>, <%s>", req.UnsubscribeMailto, unsubURL)
		} else {
			listUnsubscribeVal = fmt.Sprintf("<%s>", unsubURL)
		}

		headers := make(map[string]string)
		// Custom headers were validated above; copy them through unchanged.
		for k, v := range req.CustomHeaders {
			headers[k] = v
		}

		// Inject standard unsubscribe headers
		headers["List-Unsubscribe"] = listUnsubscribeVal
		// RFC 8058 One-Click Unsubscribe post header
		headers["List-Unsubscribe-Post"] = "List-Unsubscribe=One-Click"
		if req.CampaignID != "" {
			headers["X-Campaign-ID"] = req.CampaignID
		}

		// Template placeholders replacement
		bodyHTML := strings.ReplaceAll(req.BodyHTML, "{{UnsubscribeURL}}", unsubURL)
		bodyHTML = strings.ReplaceAll(bodyHTML, "{{RecipientEmail}}", norm)
		bodyText := strings.ReplaceAll(req.BodyText, "{{UnsubscribeURL}}", unsubURL)
		bodyText = strings.ReplaceAll(bodyText, "{{RecipientEmail}}", norm)

		msg := &PreparedMarketingMessage{
			ID:               marketingMessageID(req.TenantID, req.CampaignID, norm, req.Subject, bodyHTML, bodyText),
			TenantID:         req.TenantID,
			CampaignID:       req.CampaignID,
			Recipient:        norm,
			Subject:          req.Subject,
			BodyHTML:         bodyHTML,
			BodyText:         bodyText,
			Headers:          headers,
			UnsubscribeToken: token,
			UnsubscribeURL:   unsubURL,
		}

		result.Messages = append(result.Messages, msg)
	}

	return result, nil
}

// SenderFunc abstracts the physical mail delivery mechanism.
type SenderFunc func(ctx context.Context, ch *OutboundChannel, msg *PreparedMarketingMessage) error

// maxTrackedRecords caps every outcome list (sent/dropped/dead/uncertain).
// The cap keeps long-running in-memory queues from growing without bound;
// once the cap is reached the oldest record is evicted and counted in the
// matching overflow counter. Durable outcome recording belongs to the store
// layer, not to this queue.
const maxTrackedRecords = 1000

// DispatchQueueStats is a point-in-time snapshot of queue bookkeeping.
type DispatchQueueStats struct {
	Queued    int `json:"queued"`
	Sent      int `json:"sent"`
	Dropped   int `json:"dropped"`
	Dead      int `json:"dead"`
	Uncertain int `json:"uncertain"`
	// SentOverflow/DroppedOverflow/DeadOverflow count records evicted after
	// their list reached maxTrackedRecords.
	SentOverflow    int64 `json:"sent_overflow"`
	DroppedOverflow int64 `json:"dropped_overflow"`
	DeadOverflow    int64 `json:"dead_overflow"`
}

// DispatchQueue manages enqueued marketing messages, rate limiting, and channel enforcement.
// Send concurrency notes: DispatchNext holds the queue lock only for the
// dequeue snapshot/lease; the external sender call happens without the lock.
type DispatchQueue struct {
	mu                 sync.Mutex
	channel            *OutboundChannel
	limiter            *RateLimiter
	suppression        *SuppressionManager
	policy             SenderPolicy
	queue              []*PreparedMarketingMessage
	sent               []*PreparedMarketingMessage
	dropped            []*PreparedMarketingMessage
	dead               []*PreparedMarketingMessage
	uncertain          []*PreparedMarketingMessage
	sentOverflow       int64
	droppedOverflow    int64
	deadOverflow       int64
	uncertainOverflow  int64
	sender             SenderFunc
	maxAttempts        int
	retryBase          time.Duration
	clock              func() time.Time
	durableSuppression SuppressionChecker
}

// recordCapped appends msg to a capped outcome list, evicting and counting the
// oldest record once maxTrackedRecords is reached. Caller holds dq.mu.
func recordCapped(list *[]*PreparedMarketingMessage, overflow *int64, msg *PreparedMarketingMessage) {
	if len(*list) >= maxTrackedRecords {
		*list = (*list)[1:]
		*overflow++
	}
	*list = append(*list, msg)
}

// NewDispatchQueue creates a new DispatchQueue instance.
// A nil suppression manager disables in-memory suppression checks.
func NewDispatchQueue(ch *OutboundChannel, limiter *RateLimiter, suppression *SuppressionManager, sender SenderFunc) *DispatchQueue {
	q := &DispatchQueue{
		channel:     ch,
		limiter:     limiter,
		suppression: suppression,
		queue:       make([]*PreparedMarketingMessage, 0),
		sent:        make([]*PreparedMarketingMessage, 0),
		dropped:     make([]*PreparedMarketingMessage, 0),
		dead:        make([]*PreparedMarketingMessage, 0),
		sender:      sender,
		maxAttempts: defaultMaxSendAttempts,
		retryBase:   defaultRetryBaseDelay,
		clock:       func() time.Time { return time.Now().UTC() },
	}
	return q
}

// SetRetryPolicy overrides max send attempts and the exponential backoff base
// delay. maxAttempts <= 0 keeps the current value; base <= 0 keeps the default.
func (dq *DispatchQueue) SetRetryPolicy(maxAttempts int, base time.Duration) {
	dq.mu.Lock()
	defer dq.mu.Unlock()
	if maxAttempts > 0 {
		dq.maxAttempts = maxAttempts
	}
	if base > 0 {
		dq.retryBase = base
	}
}

// SetClock sets a custom clock for deterministic backoff testing.
func (dq *DispatchQueue) SetClock(fn func() time.Time) {
	dq.mu.Lock()
	defer dq.mu.Unlock()
	if fn != nil {
		dq.clock = fn
	}
}

// DeadCount returns the number of messages that exhausted all send attempts.
func (dq *DispatchQueue) DeadCount() int {
	dq.mu.Lock()
	defer dq.mu.Unlock()
	return len(dq.dead)
}

// SetChannel updates the outbound channel on the queue.
func (dq *DispatchQueue) SetChannel(ch *OutboundChannel) {
	dq.mu.Lock()
	defer dq.mu.Unlock()
	dq.channel = ch
}

// SetSuppressionChecker installs a tenant-qualified durable checker for egress.
// The checker is REQUIRED before the queue will send anything; without it the
// queue fails closed with ErrSuppressionCheckerRequired.
func (dq *DispatchQueue) SetSuppressionChecker(checker SuppressionChecker) {
	dq.mu.Lock()
	defer dq.mu.Unlock()
	dq.durableSuppression = checker
}

// SetSenderPolicy installs the tenant SMTP-policy hook (urmail_mail_allowed
// plus the tenant daily send limit). The policy is REQUIRED before the queue
// will send anything; without it the queue fails closed with
// ErrSenderPolicyRequired.
func (dq *DispatchQueue) SetSenderPolicy(p SenderPolicy) {
	dq.mu.Lock()
	defer dq.mu.Unlock()
	dq.policy = p
}

// UncertainCount returns the number of messages whose delivery outcome is
// unknown; they are never re-sent automatically.
func (dq *DispatchQueue) UncertainCount() int {
	dq.mu.Lock()
	defer dq.mu.Unlock()
	return len(dq.uncertain)
}

// Stats returns a snapshot of the queue bookkeeping, including overflow
// counters for capped outcome lists.
func (dq *DispatchQueue) Stats() DispatchQueueStats {
	dq.mu.Lock()
	defer dq.mu.Unlock()
	return DispatchQueueStats{
		Queued:          len(dq.queue),
		Sent:            len(dq.sent),
		Dropped:         len(dq.dropped),
		Dead:            len(dq.dead),
		Uncertain:       len(dq.uncertain),
		SentOverflow:    dq.sentOverflow,
		DroppedOverflow: dq.droppedOverflow,
		DeadOverflow:    dq.deadOverflow,
	}
}

// SetRateLimiter updates the rate limiter on the queue.
func (dq *DispatchQueue) SetRateLimiter(l *RateLimiter) {
	dq.mu.Lock()
	defer dq.mu.Unlock()
	dq.limiter = l
}

// Enqueue adds messages to the dispatch queue.
func (dq *DispatchQueue) Enqueue(msgs ...*PreparedMarketingMessage) {
	dq.mu.Lock()
	defer dq.mu.Unlock()
	dq.queue = append(dq.queue, msgs...)
}

// QueueLength returns number of pending messages.
func (dq *DispatchQueue) QueueLength() int {
	dq.mu.Lock()
	defer dq.mu.Unlock()
	return len(dq.queue)
}

// SentCount returns number of successfully sent messages.
func (dq *DispatchQueue) SentCount() int {
	dq.mu.Lock()
	defer dq.mu.Unlock()
	return len(dq.sent)
}

// DroppedCount returns number of messages dropped due to suppression.
func (dq *DispatchQueue) DroppedCount() int {
	dq.mu.Lock()
	defer dq.mu.Unlock()
	return len(dq.dropped)
}

// DispatchNext attempts to deliver the next message from the queue.
// P08 channel rules and suppression checks are enforced on every dispatch.
// Lock discipline: the queue mutex is held only while leasing (dequeuing) a
// message snapshot; the external sender call runs WITHOUT the lock so slow
// SMTP exchanges never block queue bookkeeping. Failures requeue the message
// with exponential backoff; after maxAttempts failures the message is moved to
// the dead list and never retried again.
func (dq *DispatchQueue) DispatchNext(ctx context.Context) (*PreparedMarketingMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// ---- Phase 1: lease a message under the lock (snapshot/dequeue only) ----
	dq.mu.Lock()

	// Channel check per P08: fail immediately if not configured or transactional.
	if err := ValidateMarketingChannel(dq.channel); err != nil {
		dq.mu.Unlock()
		return nil, err
	}

	// Fail closed: a sending queue MUST have a durable suppression checker and
	// a tenant sender policy installed. Without them the queue refuses to send.
	if dq.durableSuppression == nil {
		dq.mu.Unlock()
		return nil, ErrSuppressionCheckerRequired
	}
	if dq.policy == nil {
		dq.mu.Unlock()
		return nil, ErrSenderPolicyRequired
	}
	if dq.sender == nil {
		dq.mu.Unlock()
		return nil, ErrSenderRequired
	}

	now := dq.clock()
	idx := -1
	var msg *PreparedMarketingMessage
	for i, m := range dq.queue {
		if m.NextRetryAt.After(now) {
			continue // still backing off
		}
		idx = i
		msg = m
		break
	}
	if idx == -1 {
		dq.mu.Unlock()
		return nil, ErrQueueEmpty
	}

	checker := dq.durableSuppression
	policy := dq.policy
	channel := dq.channel
	// Suppression re-check at lease time (still under lock, pure in-memory).
	if dq.suppression != nil && dq.suppression.CheckSuppression(msg.TenantID, msg.Recipient) {
		dq.queue = append(dq.queue[:idx], dq.queue[idx+1:]...)
		recordCapped(&dq.dropped, &dq.droppedOverflow, msg)
		dq.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrSuppressedRecipient, msg.Recipient)
	}

	// Lease: remove from queue, release the lock before any I/O.
	dq.queue = append(dq.queue[:idx], dq.queue[idx+1:]...)
	dq.mu.Unlock()

	// Durable suppression check, fail closed: a checker error or unknown result
	// is treated as SUPPRESSED and the message is dropped, never sent.
	suppressed, checkErr := checker.IsSuppressed(ctx, msg.TenantID, msg.Recipient)
	if checkErr != nil {
		dq.mu.Lock()
		recordCapped(&dq.dropped, &dq.droppedOverflow, msg)
		dq.mu.Unlock()
		return nil, fmt.Errorf("%w: durable suppression check for %s: %w", ErrSuppressedRecipient, msg.Recipient, checkErr)
	}
	if suppressed {
		dq.mu.Lock()
		recordCapped(&dq.dropped, &dq.droppedOverflow, msg)
		dq.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrSuppressedRecipient, msg.Recipient)
	}

	// Tenant SMTP-policy hook (allow-list + daily quota): fail closed. A policy
	// error or denial skips and records the message instead of sending.
	allowed, policyErr := policy.AllowedToSend(msg.TenantID)
	if policyErr != nil {
		dq.mu.Lock()
		recordCapped(&dq.dropped, &dq.droppedOverflow, msg)
		dq.mu.Unlock()
		return nil, fmt.Errorf("%w: tenant %d policy check: %w", ErrSenderPolicyDenied, msg.TenantID, policyErr)
	}
	if !allowed {
		dq.mu.Lock()
		recordCapped(&dq.dropped, &dq.droppedOverflow, msg)
		dq.mu.Unlock()
		return nil, fmt.Errorf("%w: tenant %d", ErrSenderPolicyDenied, msg.TenantID)
	}

	// Reserve a per-tenant rate-limit token before sending. On rate-limit
	// rejection the message is re-queued at the head BY IDENTITY, never by a
	// stale index (other workers may have changed the queue meanwhile).
	dq.mu.Lock()
	if dq.limiter != nil {
		if err := dq.limiter.TakeFor(msg.TenantID, 1); err != nil {
			dq.queue = append([]*PreparedMarketingMessage{msg}, dq.queue...)
			dq.mu.Unlock()
			return nil, err
		}
	}
	dq.mu.Unlock()

	sendErr := dq.sender(ctx, channel, msg)

	// ---- Phase 2: record the outcome back under the lock ----
	dq.mu.Lock()
	if sendErr == nil {
		msg.Attempts++
		msg.LastError = ""
		msg.NextRetryAt = time.Time{}
		recordCapped(&dq.sent, &dq.sentOverflow, msg)
		dq.mu.Unlock()
		return msg, nil
	}

	if IsDeliveryUncertain(sendErr) {
		// Unknown outcome: the message may have been delivered. Never silently
		// re-send it; record it as uncertain for operator triage.
		msg.Attempts++
		msg.LastError = sendErr.Error()
		recordCapped(&dq.uncertain, &dq.uncertainOverflow, msg)
		dq.mu.Unlock()
		return nil, fmt.Errorf("%w: %s (attempts=%d): %w", ErrDeliveryUncertain, msg.ID, msg.Attempts, sendErr)
	}

	// Definite failure: return the reserved rate-limit token.
	if dq.limiter != nil {
		dq.limiter.RefundFor(msg.TenantID, 1)
	}

	msg.Attempts++
	msg.LastError = sendErr.Error()
	if msg.Attempts >= dq.maxAttempts {
		// Dead-letter: stop retrying forever.
		recordCapped(&dq.dead, &dq.deadOverflow, msg)
		dq.mu.Unlock()
		return nil, fmt.Errorf("%w: %s (attempts=%d): %w", ErrMessageDead, msg.ID, msg.Attempts, sendErr)
	}
	// Exponential backoff, requeued at the tail so other messages proceed.
	msg.NextRetryAt = now.Add(retryBackoff(dq.retryBase, msg.Attempts))
	dq.queue = append(dq.queue, msg)
	dq.mu.Unlock()
	return nil, sendErr
}

// DispatchBatch processes up to maxCount messages from the queue.
func (dq *DispatchQueue) DispatchBatch(ctx context.Context, maxCount int) (int, error) {
	sent := 0
	for (maxCount <= 0 || sent < maxCount) && dq.QueueLength() > 0 {
		msg, err := dq.DispatchNext(ctx)
		if errors.Is(err, ErrQueueEmpty) {
			break
		}
		if err != nil {
			if errors.Is(err, ErrSuppressedRecipient) {
				// Suppressed message dropped; continue with next in queue
				continue
			}
			return sent, err
		}
		if msg == nil {
			break
		}
		sent++
	}
	return sent, nil
}

// DispatchMarketingMessage dispatches one message through the same required
// channel, durable suppression, policy, sender and tenant-limit gates as a queue.
func DispatchMarketingMessage(ctx context.Context, ch *OutboundChannel, msg *PreparedMarketingMessage, limiter *RateLimiter, suppression *SuppressionManager, checker SuppressionChecker, policy SenderPolicy, sender SenderFunc) error {
	return DispatchMarketingMessageWithChecker(ctx, ch, msg, limiter, suppression, checker, policy, sender)
}

// DispatchMarketingMessageWithChecker enforces every egress gate before sending.
// A missing checker, policy or physical sender is a configuration failure.
func DispatchMarketingMessageWithChecker(ctx context.Context, ch *OutboundChannel, msg *PreparedMarketingMessage, limiter *RateLimiter, suppression *SuppressionManager, checker SuppressionChecker, policy SenderPolicy, sender SenderFunc) error {
	if err := ValidateMarketingChannel(ch); err != nil {
		return err
	}
	if msg == nil || msg.TenantID <= 0 {
		return errors.New("valid tenant-qualified marketing message is required")
	}
	if checker == nil {
		return ErrSuppressionCheckerRequired
	}
	if policy == nil {
		return ErrSenderPolicyRequired
	}
	if sender == nil {
		return ErrSenderRequired
	}
	if suppression != nil && suppression.CheckSuppression(msg.TenantID, msg.Recipient) {
		return ErrSuppressedRecipient
	}
	suppressed, err := checker.IsSuppressed(ctx, msg.TenantID, msg.Recipient)
	if err != nil {
		return fmt.Errorf("%w: durable check failed", ErrSuppressedRecipient)
	}
	if suppressed {
		return ErrSuppressedRecipient
	}
	allowed, err := policy.AllowedToSend(msg.TenantID)
	if err != nil || !allowed {
		return ErrSenderPolicyDenied
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if limiter != nil {
		if err := limiter.TakeFor(msg.TenantID, 1); err != nil {
			return err
		}
	}
	if err := sender(ctx, ch, msg); err != nil {
		if limiter != nil && !IsDeliveryUncertain(err) {
			limiter.RefundFor(msg.TenantID, 1)
		}
		return err
	}
	return nil
}

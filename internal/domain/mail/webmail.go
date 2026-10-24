package mail

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Webmail SSO errors.
var (
	ErrWebmailSecretRequired = errors.New("webmail secret key is required")
	ErrWebmailTenantRequired = errors.New("webmail tenant_id is required")
	ErrWebmailEmailRequired  = errors.New("webmail email is required")
	ErrWebmailInvalidToken   = errors.New("invalid webmail sso token")
	ErrWebmailTokenExpired   = errors.New("webmail sso token has expired")
	ErrWebmailTenantMismatch = errors.New("webmail token tenant mismatch")
	ErrWebmailTokenRevoked   = errors.New("webmail sso token has been revoked")
	ErrWebmailScopeMismatch  = errors.New("webmail token scope mismatch")
	ErrWebmailInvalidTTL     = errors.New("webmail token ttl exceeds maximum")
)

// DefaultWebmailTokenScope is the scope stamped on newly issued tokens.
const DefaultWebmailTokenScope = "webmail"

// revokedJTI records when a revoked token id expires so the table can be pruned.
type revokedJTI struct {
	expiresAt int64
}

// webmailRevocations is the in-memory revocation table mapping jti -> expiry.
// TODO(multi-instance): this only guards the current process. For multi-instance
// deployments the revocation list must be moved to PostgreSQL (e.g. a
// `webmail_revoked_jtis` table with expiry-based cleanup) so a token revoked on
// one node is rejected everywhere.
var (
	webmailRevocationsMu sync.Mutex
	webmailRevocations   = map[string]revokedJTI{}
)

// RevokeWebmailToken revokes a token by its jti until the given expiry instant.
// Validation rejects any token whose jti is present in the revocation table.
func RevokeWebmailToken(jti string, until time.Time) {
	jti = strings.TrimSpace(jti)
	if jti == "" {
		return
	}
	webmailRevocationsMu.Lock()
	defer webmailRevocationsMu.Unlock()
	pruneWebmailRevocationsLocked(time.Now().Unix())
	webmailRevocations[jti] = revokedJTI{expiresAt: until.Unix()}
}

// IsWebmailTokenRevoked reports whether the jti is currently revoked.
func IsWebmailTokenRevoked(jti string) bool {
	now := time.Now().Unix()
	webmailRevocationsMu.Lock()
	defer webmailRevocationsMu.Unlock()
	r, ok := webmailRevocations[jti]
	if !ok {
		return false
	}
	if r.expiresAt > 0 && now >= r.expiresAt {
		delete(webmailRevocations, jti) // expired revocation entry
		return false
	}
	return true
}

// pruneWebmailRevocationsLocked drops expired entries. Caller holds the mutex.
func pruneWebmailRevocationsLocked(now int64) {
	for k, r := range webmailRevocations {
		if r.expiresAt > 0 && now >= r.expiresAt {
			delete(webmailRevocations, k)
		}
	}
}

// DefaultWebmailTokenTTL is the default validity duration for an SSO token (5 minutes).
const DefaultWebmailTokenTTL = 5 * time.Minute

// MaxWebmailTokenTTL bounds token lifetime so leaked SSO credentials cannot
// grant long-lived access and Unix-second expiry cannot overflow.
const MaxWebmailTokenTTL = 15 * time.Minute

// WebmailSSOToken holds claims embedded in the Roundcube single-sign-on token.
type WebmailSSOToken struct {
	TenantID  string `json:"tenant_id"`
	Email     string `json:"email"`
	UserID    string `json:"user_id,omitempty"`
	IssuedAt  int64  `json:"iat"` // Unix epoch seconds
	ExpiresAt int64  `json:"exp"` // Unix epoch seconds
	Nonce     string `json:"nonce"`
	// JTI is a random per-token identifier used for revocation; validation
	// rejects tokens without one.
	JTI string `json:"jti"`
	// Scope restricts what the token authorizes (e.g. "webmail").
	Scope string `json:"scope"`
	// Mailbox is the exact mailbox (email) the token grants access to; it is
	// verified to equal Email on validation to prevent claim confusion.
	Mailbox string `json:"mailbox"`
}

// GenerateWebmailSSOToken creates a short-lived HMAC-SHA256 signed SSO token for Roundcube webmail.
// If ttl <= 0, DefaultWebmailTokenTTL is used.
func GenerateWebmailSSOToken(secret []byte, tenantID, email string, ttl time.Duration) (string, error) {
	return GenerateWebmailSSOTokenWithUser(secret, tenantID, "", email, ttl)
}

// GenerateWebmailToken is an alias for GenerateWebmailSSOToken.
func GenerateWebmailToken(secret []byte, tenantID, email string, ttl time.Duration) (string, error) {
	return GenerateWebmailSSOToken(secret, tenantID, email, ttl)
}

// GenerateWebmailSSOTokenWithUser creates an SSO token including optional userID.
// Each token carries a random jti (for revocation), the "webmail" scope, and
// the mailbox (email) it grants access to.
func GenerateWebmailSSOTokenWithUser(secret []byte, tenantID, userID, email string, ttl time.Duration) (string, error) {
	if len(secret) == 0 {
		return "", ErrWebmailSecretRequired
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return "", ErrWebmailTenantRequired
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return "", ErrWebmailEmailRequired
	}
	if ttl <= 0 {
		ttl = DefaultWebmailTokenTTL
	}
	if ttl > MaxWebmailTokenTTL {
		return "", ErrWebmailInvalidTTL
	}

	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", fmt.Errorf("failed to generate random nonce: %w", err)
	}
	jtiBytes := make([]byte, 16)
	if _, err := rand.Read(jtiBytes); err != nil {
		return "", fmt.Errorf("failed to generate jti: %w", err)
	}

	now := time.Now().Unix()
	token := WebmailSSOToken{
		TenantID:  tenantID,
		UserID:    strings.TrimSpace(userID),
		Email:     email,
		IssuedAt:  now,
		ExpiresAt: now + int64(ttl.Seconds()),
		Nonce:     hex.EncodeToString(nonceBytes),
		JTI:       hex.EncodeToString(jtiBytes),
		Scope:     DefaultWebmailTokenScope,
		Mailbox:   email,
	}

	payloadBytes, err := json.Marshal(token)
	if err != nil {
		return "", fmt.Errorf("failed to marshal webmail token: %w", err)
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(payloadBytes)
	sigBytes := mac.Sum(nil)

	payloadPart := base64.RawURLEncoding.EncodeToString(payloadBytes)
	sigPart := base64.RawURLEncoding.EncodeToString(sigBytes)

	return payloadPart + "." + sigPart, nil
}

// ValidateWebmailSSOToken validates a signed webmail SSO token, verifying the HMAC signature,
// expiration time, and ensuring it matches the expected tenantID (if non-empty).
func ValidateWebmailSSOToken(secret []byte, expectedTenantID, tokenString string) (*WebmailSSOToken, error) {
	if len(secret) == 0 {
		return nil, ErrWebmailSecretRequired
	}
	tokenString = strings.TrimSpace(tokenString)
	if tokenString == "" {
		return nil, ErrWebmailInvalidToken
	}

	parts := strings.Split(tokenString, ".")
	if len(parts) != 2 {
		return nil, fmt.Errorf("%w: token must have payload and signature parts", ErrWebmailInvalidToken)
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("%w: invalid payload base64: %w", ErrWebmailInvalidToken, err)
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%w: invalid signature base64: %w", ErrWebmailInvalidToken, err)
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(payloadBytes)
	expectedSig := mac.Sum(nil)

	if !hmac.Equal(sigBytes, expectedSig) {
		return nil, fmt.Errorf("%w: signature mismatch", ErrWebmailInvalidToken)
	}

	var token WebmailSSOToken
	if err := json.Unmarshal(payloadBytes, &token); err != nil {
		return nil, fmt.Errorf("%w: invalid token json: %w", ErrWebmailInvalidToken, err)
	}

	now := time.Now().Unix()
	if now >= token.ExpiresAt {
		return nil, ErrWebmailTokenExpired
	}

	// jti is mandatory: tokens without one cannot be revoked or audited.
	if strings.TrimSpace(token.JTI) == "" {
		return nil, fmt.Errorf("%w: missing jti claim", ErrWebmailInvalidToken)
	}

	// Scope and mailbox claims must be present and consistent.
	if strings.TrimSpace(token.Scope) == "" {
		return nil, fmt.Errorf("%w: missing scope claim", ErrWebmailInvalidToken)
	}
	if strings.TrimSpace(token.Mailbox) == "" {
		return nil, fmt.Errorf("%w: missing mailbox claim", ErrWebmailInvalidToken)
	}
	if token.Mailbox != token.Email {
		return nil, fmt.Errorf("%w: mailbox %q does not match email %q", ErrWebmailInvalidToken, token.Mailbox, token.Email)
	}

	// Revocation check by jti.
	if IsWebmailTokenRevoked(token.JTI) {
		return nil, ErrWebmailTokenRevoked
	}

	expectedTenantID = strings.TrimSpace(expectedTenantID)
	if expectedTenantID != "" && token.TenantID != expectedTenantID {
		return nil, fmt.Errorf("%w: expected %q, got %q", ErrWebmailTenantMismatch, expectedTenantID, token.TenantID)
	}

	return &token, nil
}

// ValidateWebmailSSOTokenWithScope validates a token and additionally requires
// its scope claim to equal expectedScope (non-empty).
func ValidateWebmailSSOTokenWithScope(secret []byte, expectedTenantID, expectedScope, tokenString string) (*WebmailSSOToken, error) {
	token, err := ValidateWebmailSSOToken(secret, expectedTenantID, tokenString)
	if err != nil {
		return nil, err
	}
	expectedScope = strings.TrimSpace(expectedScope)
	if expectedScope != "" && token.Scope != expectedScope {
		return nil, fmt.Errorf("%w: expected %q, got %q", ErrWebmailScopeMismatch, expectedScope, token.Scope)
	}
	return token, nil
}

// ValidateWebmailToken is an alias for ValidateWebmailSSOToken.
func ValidateWebmailToken(secret []byte, expectedTenantID, tokenString string) (*WebmailSSOToken, error) {
	return ValidateWebmailSSOToken(secret, expectedTenantID, tokenString)
}

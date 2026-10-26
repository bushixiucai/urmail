package handlers

// MFA (TOTP) second factor, design section 18 M4.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

const (
	// mfaIssuer is the issuer shown in authenticator apps.
	mfaIssuer = "urMail.cn"
	// mfaTokenTTL bounds how long a first-factor-approved login may wait
	// for its second factor.
	mfaTokenTTL = 5 * time.Minute
	// mfaMaxVerifyAttempts invalidates the pending token after repeated
	// wrong codes, bounding online guessing even under the shared limiter.
	mfaMaxVerifyAttempts = 3
	// mfaRecoveryCodeCount is how many one-time recovery codes are issued
	// when MFA is enabled.
	mfaRecoveryCodeCount = 10
)

// pendingMFALogin is a first-factor-authenticated login awaiting its second
// factor.
type pendingMFALogin struct {
	CredentialVersion int64
	UserID            int64
	LimiterKey        string
	ExpiresAt         time.Time
	Attempts          int
}

// mfaTokenStore holds pending MFA logins in memory, keyed by a one-time
// random token. SECURITY LIMITATION: this is per-process state, so it only
// works while urmaild runs as a single instance; a multi-instance deployment
// must move it to PostgreSQL or Redis.
type mfaTokenStore struct {
	mu         sync.Mutex
	entries    map[string]pendingMFALogin
	now        func() time.Time
	maxEntries int
	maxPerUser int
}

var pendingMFATokens = newMFATokenStore()

var mfaCapacityRejections = promauto.NewCounter(prometheus.CounterOpts{
	Name: "urmail_auth_mfa_capacity_rejections_total",
	Help: "Pending MFA challenges rejected because a process or per-user capacity limit was reached.",
})

func newMFATokenStore() *mfaTokenStore {
	return &mfaTokenStore{entries: make(map[string]pendingMFALogin), now: time.Now, maxEntries: 10000, maxPerUser: 3}
}

// issue registers a pending second-factor login and returns its token.
func (s *mfaTokenStore) issue(userID int64, limiterKey string, versions ...int64) (string, error) {
	token, err := randomHexToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	count := 0
	for _, entry := range s.entries {
		if entry.UserID == userID {
			count++
		}
	}
	if len(s.entries) >= s.maxEntries || count >= s.maxPerUser {
		mfaCapacityRejections.Inc()
		return "", errors.New("MFA challenge capacity exhausted")
	}
	version := int64(0)
	if len(versions) > 0 {
		version = versions[0]
	}
	s.entries[token] = pendingMFALogin{CredentialVersion: version,
		UserID:     userID,
		LimiterKey: limiterKey,
		ExpiresAt:  s.now().Add(mfaTokenTTL),
	}
	return token, nil
}

// check validates the token and records a failed verification attempt. After
// mfaMaxVerifyAttempts wrong codes the token is invalidated. The token is
// not consumed: take does that once the second factor is proven.
func (s *mfaTokenStore) check(token string) (pendingMFALogin, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.entries[token]
	if !ok || !s.now().Before(p.ExpiresAt) {
		delete(s.entries, token)
		return pendingMFALogin{}, false
	}
	if p.Attempts >= mfaMaxVerifyAttempts {
		return pendingMFALogin{}, false
	}
	p.Attempts++
	s.entries[token] = p
	return p, true
}

// take atomically consumes the token. Every token grants at most one session.
func (s *mfaTokenStore) take(token string) (pendingMFALogin, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.entries[token]
	delete(s.entries, token)
	if !ok || !s.now().Before(p.ExpiresAt) {
		return pendingMFALogin{}, false
	}
	return p, true
}

// generateRecoveryCodes returns n one-time recovery codes formatted as
// xxxxxx-xxxxxx. Only their SHA-256 hashes are persisted.
func generateRecoveryCodes(n int) ([]string, error) {
	codes := make([]string, n)
	for i := range codes {
		b := make([]byte, 6)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		codes[i] = hex.EncodeToString(b[:3]) + "-" + hex.EncodeToString(b[3:])
	}
	return codes, nil
}

// hashRecoveryCode normalises and hashes a recovery code the way it is
// stored in users.mfa_recovery_codes.
func hashRecoveryCode(code string) string {
	normalised := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	sum := sha256.Sum256([]byte(normalised))
	return hex.EncodeToString(sum[:])
}

// validateTOTP checks a 6-digit code against the user's secret, allowing one
// period of clock drift.
func validateTOTP(code, secret string) bool {
	if secret == "" {
		return false
	}
	ok, err := totp.ValidateCustom(strings.TrimSpace(code), secret, time.Now(), totp.ValidateOpts{
		Period:    30,
		Skew:      1,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	return err == nil && ok
}

// MFACodeRequest supplies a TOTP or a one-time recovery code.
type MFACodeRequest struct {
	Code string `json:"code" binding:"required"`
}

// MFASetup generates a TOTP secret for the logged-in user, stores it without
// enabling MFA, and returns the secret plus its otpauth:// URL.
func (h *AuthHandler) MFASetup(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if h.Secrets == nil {
		authError(c, 503, "INTERNAL_ERROR")
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	user, err := h.Store.GetUserByID(c, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "USER_NOT_FOUND"}})
		return
	}
	if user.MfaEnabled {
		c.JSON(http.StatusConflict, gin.H{
			"error": gin.H{"code": "MFA_ALREADY_ENABLED", "message": "auth.mfa_already_enabled"},
		})
		return
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      mfaIssuer,
		AccountName: user.Email,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR"}})
		return
	}
	storedSecret := key.Secret()
	if h.Secrets != nil {
		storedSecret, err = h.Secrets.EncryptString(storedSecret)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR"}})
			return
		}
	}
	if _, err = h.Store.PlatformChangePanelMFA(c, store.PlatformChangePanelMFAParams{UserID: userID, ExpectedVersion: user.CredentialVersion, Secret: pgtype.Text{String: storedSecret, Valid: true}, Enabled: false, Codes: []byte("[]"), KeepSession: keepSessionID(c)}); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	pendingMFATokens.revokeUser(userID)

	c.JSON(http.StatusOK, gin.H{
		"secret":      key.Secret(),
		"otpauth_url": key.URL(),
		"mfa_enabled": false,
	})
}

// MFAEnable verifies the first TOTP code and turns MFA on, issuing the
// one-time recovery codes in the response (they are only ever shown once).
func (h *AuthHandler) MFAEnable(c *gin.Context) {
	var req MFACodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "INVALID_PARAMS", "message": "common.invalid_params"},
		})
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	user, err := h.Store.GetUserByID(c, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "USER_NOT_FOUND"}})
		return
	}
	if user.MfaEnabled {
		c.JSON(http.StatusConflict, gin.H{
			"error": gin.H{"code": "MFA_ALREADY_ENABLED", "message": "auth.mfa_already_enabled"},
		})
		return
	}
	if !user.MfaSecret.Valid || user.MfaSecret.String == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "MFA_NO_PENDING_SETUP", "message": "auth.mfa_no_pending_setup"},
		})
		return
	}
	mfaSecret, err := h.decryptMFA(user.MfaSecret.String)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR"}})
		return
	}
	if !validateTOTP(req.Code, mfaSecret) {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "MFA_INVALID_CODE", "message": "auth.mfa_invalid_code"},
		})
		return
	}

	codes, err := generateRecoveryCodes(mfaRecoveryCodeCount)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR"}})
		return
	}
	hashes := make([]string, len(codes))
	for i, code := range codes {
		hashes[i] = hashRecoveryCode(code)
	}
	hashesJSON, err := json.Marshal(hashes)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR"}})
		return
	}
	if _, err = h.Store.PlatformChangePanelMFA(c, store.PlatformChangePanelMFAParams{UserID: userID, ExpectedVersion: user.CredentialVersion, Secret: user.MfaSecret, Enabled: true, Codes: hashesJSON, KeepSession: keepSessionID(c)}); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	pendingMFATokens.revokeUser(userID)

	c.JSON(http.StatusOK, gin.H{
		"status":         "ok",
		"mfa_enabled":    true,
		"recovery_codes": codes,
	})
}

// MFADisable turns MFA off. The current TOTP code or an unused recovery code
// must be presented.
func (h *AuthHandler) MFADisable(c *gin.Context) {
	var req MFACodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "INVALID_PARAMS", "message": "common.invalid_params"},
		})
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	user, err := h.Store.GetUserByID(c, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "USER_NOT_FOUND"}})
		return
	}
	if !user.MfaEnabled {
		c.JSON(http.StatusConflict, gin.H{
			"error": gin.H{"code": "MFA_NOT_ENABLED", "message": "auth.mfa_not_enabled"},
		})
		return
	}

	// AUTH-01: without a per-user failure budget an attacker holding a stolen
	// bearer token could brute-force the six-digit TOTP online. The budget is
	// tighter than login and only resets on a successful disable.
	attemptKey := mfaDisableKey(userID)
	if !mfaDisableAttempts.allowN(attemptKey, mfaDisableMaxFailures) {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error": gin.H{"code": "AUTH_RATE_LIMITED", "message": "auth.rate_limited"},
		})
		return
	}

	authenticated := false
	if user.MfaSecret.Valid {
		mfaSecret, err := h.decryptMFA(user.MfaSecret.String)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR"}})
			return
		}
		authenticated = validateTOTP(req.Code, mfaSecret)
	}
	if !authenticated {
		n, err := h.Store.ConsumeRecoveryCode(c, store.ConsumeRecoveryCodeParams{
			ID:      userID,
			Column2: hashRecoveryCode(req.Code),
		})
		if err != nil || n == 0 {
			mfaDisableAttempts.fail(attemptKey)
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"code": "MFA_INVALID_CODE", "message": "auth.mfa_invalid_code"},
			})
			return
		}
	}

	if _, err = h.Store.PlatformChangePanelMFA(c, store.PlatformChangePanelMFAParams{UserID: userID, ExpectedVersion: user.CredentialVersion, Secret: pgtype.Text{}, Enabled: false, Codes: []byte("[]"), KeepSession: keepSessionID(c)}); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	pendingMFATokens.revokeUser(userID)

	mfaDisableAttempts.reset(attemptKey)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "mfa_enabled": false})
}

// MFAVerify is the second login leg: it consumes the mfa_token issued by a
// first-factor-approved login plus a TOTP code or recovery code, and only
// then creates the session.
func (h *AuthHandler) MFAVerify(c *gin.Context) {
	var req struct {
		MFAToken string `json:"mfa_token" binding:"required"`
		Code     string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "INVALID_PARAMS", "message": "common.invalid_params"},
		})
		return
	}

	pending, ok := pendingMFATokens.check(req.MFAToken)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "MFA_TOKEN_INVALID", "message": "auth.mfa_invalid_token"},
		})
		return
	}
	ipKey := ipKeyFromLimiterKey(pending.LimiterKey)
	if !loginAttempts.allow(pending.LimiterKey) || !loginAttempts.allowN(ipKey, loginMaxIPFailures) {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error": gin.H{"code": "AUTH_RATE_LIMITED", "message": "auth.rate_limited"},
		})
		return
	}

	user, err := h.Store.GetUserByID(c, pending.UserID)
	if err != nil || !user.MfaEnabled || user.CredentialVersion != pending.CredentialVersion {
		pendingMFATokens.take(req.MFAToken)
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "INVALID_CREDENTIALS", "message": "auth.invalid_credentials"},
		})
		return
	}

	authenticated := false
	if user.MfaSecret.Valid {
		mfaSecret, decryptErr := h.decryptMFA(user.MfaSecret.String)
		if decryptErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR"}})
			return
		}
		authenticated = validateTOTP(req.Code, mfaSecret)
	}
	if !authenticated {
		n, err := h.Store.ConsumeRecoveryCode(c, store.ConsumeRecoveryCodeParams{
			ID:      pending.UserID,
			Column2: hashRecoveryCode(req.Code),
		})
		if err == nil && n > 0 {
			authenticated = true
		}
	}
	if !authenticated {
		pendingMFATokens.failed(req.MFAToken)
		loginAttempts.fail(pending.LimiterKey)
		loginAttempts.fail(ipKey)
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "MFA_INVALID_CODE", "message": "auth.mfa_invalid_code"},
		})
		return
	}

	if _, consumed := pendingMFATokens.take(req.MFAToken); !consumed {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "MFA_TOKEN_INVALID", "message": "auth.mfa_invalid_token"}})
		return
	}
	loginAttempts.reset(pending.LimiterKey)
	loginAttempts.reset(ipKey)
	h.establishSession(c, user.ID, user.Email, user.DisplayName, user.Locale, true, user.PasswordHash, pending.CredentialVersion)
}

// MFAStatus reports whether TOTP is enabled and whether a setup secret is
// pending confirmation.
func (h *AuthHandler) MFAStatus(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	user, err := h.Store.GetUserByID(c, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "USER_NOT_FOUND"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"mfa_enabled": user.MfaEnabled,
		"pending":     !user.MfaEnabled && user.MfaSecret.Valid && user.MfaSecret.String != "",
	})
}

func (s *mfaTokenStore) pruneLocked() {
	for token, entry := range s.entries {
		if !s.now().Before(entry.ExpiresAt) {
			delete(s.entries, token)
		}
	}
}
func (s *mfaTokenStore) revokeUser(userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, entry := range s.entries {
		if entry.UserID == userID {
			delete(s.entries, token)
		}
	}
}

// RunMFAReaper bounds abandoned challenges independently of further requests.
func RunMFAReaper(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pendingMFATokens.mu.Lock()
			pendingMFATokens.pruneLocked()
			pendingMFATokens.mu.Unlock()
		}
	}
}

func (s *mfaTokenStore) failed(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.entries[token]; ok && p.Attempts >= mfaMaxVerifyAttempts {
		delete(s.entries, token)
	}
}

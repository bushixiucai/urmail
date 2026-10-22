package handlers

// MFA (TOTP) second factor, design section 18 M4.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

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
	UserID     int64
	LimiterKey string
	ExpiresAt  time.Time
	Attempts   int
}

// mfaTokenStore holds pending MFA logins in memory, keyed by a one-time
// random token. SECURITY LIMITATION: this is per-process state, so it only
// works while urmaild runs as a single instance; a multi-instance deployment
// must move it to PostgreSQL or Redis.
type mfaTokenStore struct {
	mu      sync.Mutex
	entries map[string]pendingMFALogin
}

var pendingMFATokens = newMFATokenStore()

func newMFATokenStore() *mfaTokenStore {
	return &mfaTokenStore{entries: make(map[string]pendingMFALogin)}
}

// issue registers a pending second-factor login and returns its token.
func (s *mfaTokenStore) issue(userID int64, limiterKey string) (string, error) {
	token, err := randomHexToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[token] = pendingMFALogin{
		UserID:     userID,
		LimiterKey: limiterKey,
		ExpiresAt:  time.Now().Add(mfaTokenTTL),
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
	if !ok || time.Now().After(p.ExpiresAt) {
		delete(s.entries, token)
		return pendingMFALogin{}, false
	}
	p.Attempts++
	if p.Attempts >= mfaMaxVerifyAttempts {
		delete(s.entries, token)
	} else {
		s.entries[token] = p
	}
	return p, true
}

// take atomically consumes the token. Every token grants at most one session.
func (s *mfaTokenStore) take(token string) (pendingMFALogin, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.entries[token]
	delete(s.entries, token)
	if !ok || time.Now().After(p.ExpiresAt) {
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

type MFACodeRequest struct {
	Code string `json:"code" binding:"required"`
}

// MFASetup generates a TOTP secret for the logged-in user, stores it without
// enabling MFA, and returns the secret plus its otpauth:// URL.
func (h *AuthHandler) MFASetup(c *gin.Context) {
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
	if err := h.Store.SetMFASecret(c, store.SetMFASecretParams{
		ID:        userID,
		MfaSecret: pgtype.Text{String: key.Secret(), Valid: true},
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}

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
	if !validateTOTP(req.Code, user.MfaSecret.String) {
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
	if err := h.Store.EnableUserMFA(c, store.EnableUserMFAParams{
		ID:               userID,
		MfaRecoveryCodes: hashesJSON,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}

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

	authenticated := user.MfaSecret.Valid && validateTOTP(req.Code, user.MfaSecret.String)
	if !authenticated {
		n, err := h.Store.ConsumeRecoveryCode(c, store.ConsumeRecoveryCodeParams{
			ID:      userID,
			Column2: hashRecoveryCode(req.Code),
		})
		if err != nil || n == 0 {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"code": "MFA_INVALID_CODE", "message": "auth.mfa_invalid_code"},
			})
			return
		}
	}

	if err := h.Store.DisableUserMFA(c, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
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
	if !loginAttempts.allow(pending.LimiterKey) {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error": gin.H{"code": "AUTH_RATE_LIMITED", "message": "auth.rate_limited"},
		})
		return
	}

	user, err := h.Store.GetUserByID(c, pending.UserID)
	if err != nil || !user.MfaEnabled {
		pendingMFATokens.take(req.MFAToken)
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "INVALID_CREDENTIALS", "message": "auth.invalid_credentials"},
		})
		return
	}

	authenticated := user.MfaSecret.Valid && validateTOTP(req.Code, user.MfaSecret.String)
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
		loginAttempts.fail(pending.LimiterKey)
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "MFA_INVALID_CODE", "message": "auth.mfa_invalid_code"},
		})
		return
	}

	pendingMFATokens.take(req.MFAToken) // single use
	loginAttempts.reset(pending.LimiterKey)
	h.establishSession(c, user.ID, user.Email, user.DisplayName, user.Locale)
}

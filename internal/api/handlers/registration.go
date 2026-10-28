package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/panelpassword"
	"github.com/bushixiucai/urmail/internal/store"
)

// RegistrationHandler creates a restricted workspace only after contact proof.
type RegistrationHandler struct {
	Pool    *db.Pool
	Secrets SecretsBox
	BaseURL string
	Enabled bool
}

func registrationTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Register queues contact verification without granting mail access.
func (h *RegistrationHandler) Register(c *gin.Context) {
	if !h.Enabled || h.Pool == nil || h.Secrets == nil {
		authError(c, 503, "REGISTRATION_UNAVAILABLE")
		return
	}
	var req struct {
		Email    string `json:"email" binding:"required"`
		Password string `json:"password" binding:"required"`
		Name     string `json:"name" binding:"required"`
		Brand    string `json:"brand"`
	}
	if c.ShouldBindJSON(&req) != nil || len(req.Password) < 12 || len(req.Password) > 256 || len(req.Name) > 128 || strings.TrimSpace(req.Name) == "" {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	email, err := mail.ParseAddress(req.Email)
	if err != nil || email.Address != req.Email || strings.ContainsAny(req.Email, "\r\n") || len(req.Email) > 254 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	origin, err := url.Parse(h.BaseURL)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil {
		authError(c, 503, "REGISTRATION_UNAVAILABLE")
		return
	}
	hash, err := panelpassword.HashContext(c.Request.Context(), req.Password)
	if err != nil {
		authError(c, 500, "INTERNAL_ERROR")
		return
	}
	token, err := randomHexToken()
	if err != nil {
		authError(c, 500, "INTERNAL_ERROR")
		return
	}
	secret, err := h.Secrets.EncryptString(token)
	if err != nil {
		authError(c, 503, "REGISTRATION_UNAVAILABLE")
		return
	}
	tx, err := h.Pool.PlatformTx(c)
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	defer func() { _ = tx.Rollback(c) }()
	q := store.New(tx)
	if current, branded := c.Get(middleware.ContextBrandID); branded {
		host, valid := normalizeBrandHost(c.Request.Host)
		registered, e := q.PublicBrandByHost(c, host)
		if !valid || e != nil || registered.ID != current.(int64) {
			authError(c, 403, "BRAND_HOST_TENANT_MISMATCH")
			return
		}
		if req.Brand == "" {
			req.Brand = registered.Slug
		}
	}
	if req.Brand == "" {
		req.Brand = "default"
	}
	brand, err := q.PublicBrandBySlug(c, req.Brand)
	if err != nil {
		authError(c, 400, "REGISTRATION_UNAVAILABLE")
		return
	}
	if current, branded := c.Get(middleware.ContextBrandID); branded && brand.ID != current.(int64) {
		authError(c, 403, "BRAND_HOST_TENANT_MISMATCH")
		return
	}
	if err = q.PlatformLockRegistrationEmail(c, req.Email); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	exists, err := q.PlatformRegistrationEmailExists(c, req.Email)
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	if !exists {
		if err = q.PlatformRevokeRegistrationRequests(c, req.Email); err != nil {
			authError(c, 500, "DB_ERROR")
			return
		}
		digest := registrationTokenHash(token)
		if err = q.PlatformCreateRegistrationRequest(c, store.PlatformCreateRegistrationRequestParams{TokenHash: digest, Email: req.Email, PasswordHash: hash, TenantName: req.Name, BrandID: brand.ID, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}); err != nil {
			authError(c, 500, "DB_ERROR")
			return
		}
		if err = q.PlatformEnqueueContactVerification(c, store.PlatformEnqueueContactVerificationParams{DedupeKey: "contact-verify:" + digest, Recipient: pgtype.Text{String: req.Email, Valid: true}, SecretPayload: pgtype.Text{String: secret, Valid: true}}); err != nil {
			authError(c, 500, "DB_ERROR")
			return
		}
	}
	if err = tx.Commit(c); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	c.JSON(202, gin.H{"status": "verification_pending", "message": "registration.verification_pending"})
}

// Verify consumes a contact proof and creates a restricted tenant.
func (h *RegistrationHandler) Verify(c *gin.Context) {
	if !h.Enabled || h.Pool == nil {
		authError(c, 503, "REGISTRATION_UNAVAILABLE")
		return
	}
	var req struct {
		Token string `json:"token" binding:"required"`
	}
	if c.ShouldBindJSON(&req) != nil || len(req.Token) != 64 {
		authError(c, 400, "VERIFICATION_INVALID")
		return
	}
	if _, err := hex.DecodeString(req.Token); err != nil {
		authError(c, 400, "VERIFICATION_INVALID")
		return
	}
	tx, err := h.Pool.PlatformTx(c)
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	defer func() { _ = tx.Rollback(c) }()
	q := store.New(tx)
	email, lookupErr := q.PlatformRegistrationTokenEmail(c, registrationTokenHash(req.Token))
	if lookupErr != nil {
		if errors.Is(lookupErr, pgx.ErrNoRows) {
			authError(c, 400, "VERIFICATION_INVALID")
		} else {
			authError(c, 500, "DB_ERROR")
		}
		return
	}
	if err = q.PlatformLockRegistrationEmail(c, email); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	pending, err := q.PlatformFindRegistrationRequest(c, registrationTokenHash(req.Token))
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 400, "VERIFICATION_INVALID")
		return
	}
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	if current, branded := c.Get(middleware.ContextBrandID); branded {
		host, valid := normalizeBrandHost(c.Request.Host)
		registered, e := q.PublicBrandByHost(c, host)
		if !valid || e != nil || registered.ID != current.(int64) || pending.BrandID != current.(int64) {
			authError(c, 403, "BRAND_HOST_TENANT_MISMATCH")
			return
		}
	}
	uid, err := q.PlatformCreateRegisteredUser(c, store.PlatformCreateRegisteredUserParams{Email: pending.Email, PasswordHash: pending.PasswordHash})
	if err != nil {
		authError(c, 409, "VERIFICATION_INVALID")
		return
	}
	tid, err := q.PlatformCreateRegisteredTenant(c, store.PlatformCreateRegisteredTenantParams{BrandID: pending.BrandID, Name: pending.TenantName, Slug: "tenant-" + uuid.NewString(), OwnerUserID: pgtype.Int8{Int64: uid, Valid: true}})
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	if err = q.PlatformRemoveRegistrationGift(c, tid); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	if err = q.PlatformBindRegisteredOwner(c, store.PlatformBindRegisteredOwnerParams{UserID: uid, TenantID: tid}); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	if err = q.PlatformSaveVerifiedContact(c, store.PlatformSaveVerifiedContactParams{TenantID: tid, Email: pending.Email}); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	if err = q.PlatformConsumeRegistrationRequest(c, pending.TokenHash); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	detail, _ := json.Marshal(map[string]any{"registration_source": "self_service", "contact_verified": true})
	if _, err = q.CreateAuditLog(c, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: tid, Valid: true}, ActorUserID: pgtype.Int8{Int64: uid, Valid: true}, Action: "CREATE registration.verified", Detail: detail}); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	if err = tx.Commit(c); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	c.JSON(201, gin.H{"tenant_id": tid, "status": "pending", "message": "registration.contact_verified"})
}

// Status reveals only whether self-service registration is enabled.
func (h *RegistrationHandler) Status(c *gin.Context) {
	c.JSON(200, gin.H{"enabled": h.Enabled && h.Pool != nil && h.Secrets != nil})
}

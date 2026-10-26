package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type mailboxNativePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// ChangePassword reuses changeMailboxPassword without Roundcube HMAC.
// The bearer token is the session proof; old_password is a fresh password proof.
func (h *MailboxNativeHandler) ChangePassword(c *gin.Context) {
	if !h.requireEnabled(c) {
		return
	}
	if h.Pool == nil {
		authError(c, http.StatusServiceUnavailable, "WEBMAIL_DISABLED")
		return
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	token := bearerToken(c)
	if token == "" {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	var req mailboxNativePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}

	tx, err := h.Pool.PlatformTx(c.Request.Context())
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	defer func() { _ = tx.Rollback(c.Request.Context()) }()
	q := store.New(tx)

	changed, passwordErr := changeMailboxPassword(c.Request.Context(), q, mailboxBridgeRequest{
		Address:     session.Address,
		Session:     token,
		OldPassword: req.OldPassword,
		NewPassword: req.NewPassword,
	})
	if passwordErr != nil {
		status, code := http.StatusServiceUnavailable, "DB_ERROR"
		switch {
		case errors.Is(passwordErr, errMailboxPasswordPolicy):
			status, code = http.StatusBadRequest, "MAILBOX_PASSWORD_POLICY"
		case errors.Is(passwordErr, errMailboxPasswordCurrent):
			status, code = http.StatusForbidden, "MAILBOX_PASSWORD_CURRENT_INVALID"
		case errors.Is(passwordErr, errMailboxPasswordConflict):
			status, code = http.StatusConflict, "MAILBOX_PASSWORD_CHANGED"
		case errors.Is(passwordErr, errMailboxPasswordRate):
			status, code = http.StatusTooManyRequests, "MAILBOX_PASSWORD_RATE_LIMITED"
		case errors.Is(passwordErr, pgx.ErrNoRows):
			status, code = http.StatusForbidden, "MAILBOX_AUTHORIZATION_DENIED"
		}
		if changed.TenantID > 0 && changed.MailboxID > 0 && status < 500 {
			detail, _ := json.Marshal(gin.H{"actor_type": "mailbox", "mailbox_id": changed.MailboxID, "status": status, "outcome": "rejected"})
			if _, auditErr := q.CreateAuditLog(c.Request.Context(), store.CreateAuditLogParams{
				TenantID:   pgtype.Int8{Int64: changed.TenantID, Valid: true},
				Action:     "mailbox.password.rejected",
				TargetType: pgtype.Text{String: "mailbox", Valid: true},
				TargetID:   pgtype.Text{String: strconv.FormatInt(changed.MailboxID, 10), Valid: true},
				Detail:     detail,
			}); auditErr != nil {
				middleware.RecordAuditFailure(true)
				authError(c, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE")
				return
			}
			if tx.Commit(c.Request.Context()) != nil {
				authError(c, http.StatusServiceUnavailable, "DB_ERROR")
				return
			}
		}
		authError(c, status, code)
		return
	}

	detail, _ := json.Marshal(gin.H{"actor_type": "mailbox", "mailbox_id": changed.MailboxID, "outcome": "changed"})
	if _, auditErr := q.CreateAuditLog(c.Request.Context(), store.CreateAuditLogParams{
		TenantID:   pgtype.Int8{Int64: changed.TenantID, Valid: true},
		Action:     "mailbox.password.changed",
		TargetType: pgtype.Text{String: "mailbox", Valid: true},
		TargetID:   pgtype.Text{String: strconv.FormatInt(changed.MailboxID, 10), Valid: true},
		Detail:     detail,
	}); auditErr != nil {
		middleware.RecordAuditFailure(true)
		zap.L().Error("write mailbox password audit", zap.Error(auditErr))
		authError(c, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE")
		return
	}
	if tx.Commit(c.Request.Context()) != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}

	if bounded, ok := h.Vault.(webmail.SessionVault); ok {
		bounded.DeleteMailbox(changed.TenantID, changed.MailboxID)
	}
	tokenHash, _ := c.Get(middleware.ContextMailboxTokenHash)
	if hash, _ := tokenHash.(string); hash != "" && h.Vault != nil {
		h.Vault.Delete(hash)
	}
	c.JSON(http.StatusOK, gin.H{
		"changed":                   true,
		"reauthentication_required": true,
	})
}

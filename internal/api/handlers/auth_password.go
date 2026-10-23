package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
)

// ChangePasswordRequest verifies the old password before rotating it.
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required,min=12,max=256"`
}

// ChangePassword rotates the signed-in user's password and invalidates all
// other sessions. The current session stays valid so the caller can continue.
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	var req ChangePasswordRequest
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
	if !mail.VerifyPassword(req.CurrentPassword, user.PasswordHash) {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "INVALID_CREDENTIALS", "message": "auth.invalid_credentials"},
		})
		return
	}
	hash, err := mail.HashPassword(req.NewPassword)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "HASH_FAILED"}})
		return
	}
	if err := h.Store.UpdateUserPassword(c, store.UpdateUserPasswordParams{
		ID:           userID,
		PasswordHash: hash,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}

	sessionID := ""
	if value, exists := c.Get(middleware.ContextSession); exists {
		if session, ok := value.(store.Sessions); ok {
			sessionID = session.ID
		}
	}
	if sessionID != "" {
		if err := h.Store.DeleteOtherUserSessions(c, store.DeleteOtherUserSessionsParams{
			UserID: userID,
			ID:     sessionID,
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

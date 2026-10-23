package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

// SessionInfo describes a revocable session without its bearer token.
type SessionInfo struct {
	ID        string `json:"id"`
	UserAgent string `json:"user_agent"`
	Current   bool   `json:"current"`
}

// ListSessions returns only the authenticated user's sessions.
func (h *AuthHandler) ListSessions(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	rows, err := h.Store.ListSessionsForUser(c, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	currentID := ""
	if value, exists := c.Get(middleware.ContextSession); exists {
		if session, ok := value.(store.Sessions); ok {
			currentID = session.ID
		}
	}
	out := make([]SessionInfo, 0, len(rows))
	for _, row := range rows {
		agent := ""
		if row.UserAgent.Valid {
			agent = row.UserAgent.String
		}
		out = append(out, SessionInfo{ID: row.ID, UserAgent: agent, Current: row.ID == currentID})
	}
	c.JSON(http.StatusOK, gin.H{"sessions": out})
}

// RevokeSession revokes only a session owned by the authenticated user.
func (h *AuthHandler) RevokeSession(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	deleted, err := h.Store.DeleteUserSession(c, store.DeleteUserSessionParams{
		UserID: userID,
		ID:     c.Param("id"),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
		return
	}
	if deleted == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

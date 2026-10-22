package middleware

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

// PlatformAdminChecker reports whether the authenticated user is a platform
// administrator. It runs on the platform pool.
type PlatformAdminChecker interface {
	CheckPlatformAdmin(ctx context.Context, userID int64) (bool, error)
}

// RequirePlatformAdmin rejects users who are not platform administrators.
func RequirePlatformAdmin(checker PlatformAdminChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := GetUserID(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
			return
		}
		admin, err := checker.CheckPlatformAdmin(c, userID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
			return
		}
		if !admin {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": gin.H{"code": "FORBIDDEN"}})
			return
		}
		c.Next()
	}
}

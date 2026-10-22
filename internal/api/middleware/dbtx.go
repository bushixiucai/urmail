package middleware

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

// ContextQueries is the gin context key holding the request-scoped
// *store.Queries bound to the tenant transaction.
const ContextQueries = "urmail.queries"

// RequireTenantTx opens a tenant-scoped database transaction for the
// request and installs request-scoped queries into the gin context.
//
// The transaction publishes urmail.tenant_id / urmail.user_id with SET
// LOCAL, so the row-level security policies apply to every query the
// handler runs. The transaction is committed after the handler returns;
// any error rolls everything back.
//
// A nil pool (unit tests without a database) disables the middleware and
// handlers fall back to their constructed stores.
func RequireTenantTx(pool *db.Pool, logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if pool == nil {
			c.Next()
			return
		}

		tenantID, ok := GetTenantID(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": gin.H{"code": "NO_TENANT_CONTEXT", "message": "auth.no_tenant"},
			})
			return
		}
		userID, _ := GetUserID(c)

		tx, err := pool.UserTx(c.Request.Context(), tenantID, userID)
		if err != nil {
			logger.Error("open tenant transaction",
				zap.Error(err),
				zap.String("path", c.Request.URL.Path),
				zap.String("request_id", c.GetString(string(RequestIDContextKey))),
			)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error": gin.H{"code": "INTERNAL_ERROR"},
			})
			return
		}
		// Rollback is a no-op after a successful commit.
		defer func() { _ = tx.Rollback(context.Background()) }()

		c.Set(ContextQueries, store.New(tx))

		c.Next()

		if err := tx.Commit(c.Request.Context()); err != nil {
			logger.Error("commit tenant transaction",
				zap.Error(err),
				zap.String("path", c.Request.URL.Path),
				zap.String("request_id", c.GetString(string(RequestIDContextKey))),
			)
			if !c.Writer.Written() {
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": gin.H{"code": "INTERNAL_ERROR"},
				})
			}
		}
	}
}

// QueriesFromCtx returns the request-scoped queries installed by
// RequireTenantTx, or nil when the middleware did not run.
func QueriesFromCtx(c *gin.Context) (any, bool) {
	v, ok := c.Get(ContextQueries)
	return v, ok
}

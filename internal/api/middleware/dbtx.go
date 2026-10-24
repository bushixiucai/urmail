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

// ContextRole is the gin context key holding the authenticated user's role
// within the tenant ("owner", "admin", ...).
const ContextRole = "urmail.role"

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
		defer func() {
			recovered := recover()
			_ = tx.Rollback(context.Background())
			delete(c.Keys, contextTransaction)
			if recovered != nil {
				if state := auditState(c); state != nil {
					state.recorded = false
					state.forcedStatus = 500
					state.phase = "panic"
					if state.businessCommitted {
						state.phase = "after_commit"
					}
				}
			}
			finishIndependentAudit(c)
			if recovered != nil {
				panic(recovered)
			}
		}()

		queries := store.New(tx)
		role, err := queries.GetUserRoleForTenant(c.Request.Context(), store.GetUserRoleForTenantParams{
			UserID:   userID,
			TenantID: tenantID,
		})
		if err != nil {
			// The authenticated user is not a member of the session's
			// tenant. Deny by default.
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": gin.H{"code": "FORBIDDEN", "message": "error.FORBIDDEN"},
			})
			return
		}
		c.Set(ContextRole, role)

		c.Set(ContextQueries, queries)

		originalWriter := c.Writer
		buffered := newResponseBuffer(originalWriter)
		c.Writer = buffered
		defer func() { c.Writer = originalWriter }()
		c.Set(contextTransaction, tx)
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			if _, err := tx.Exec(c.Request.Context(), "SELECT pg_advisory_xact_lock($1)", tenantID); err != nil {
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB_ERROR"}})
				buffered.publish()
				return
			}
		}
		c.Next()
		if failed, _ := c.Get("urmail.transaction_failed"); failed == true || buffered.overflow {
			c.Writer = originalWriter
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "RESPONSE_TOO_LARGE"}})
			return
		}
		if buffered.Status() >= 400 {
			_ = tx.Rollback(context.Background())
			buffered.publish()
			return
		}
		if err := prepareTransactionAudit(c, tx); err != nil {
			RecordAuditFailure(true)
			logger.Error("prepare tenant audit", zap.Error(err))
			c.Writer = originalWriter
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR"}})
			return
		}
		if err := tx.Commit(c.Request.Context()); err != nil {
			logger.Error("commit tenant transaction", zap.Error(err), zap.String("path", c.Request.URL.Path))
			c.Writer = originalWriter
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR"}})
			return
		}
		if state := auditState(c); state != nil {
			state.recorded = true
			state.businessCommitted = true
		}
		delete(c.Keys, ContextQueries)
		delete(c.Keys, contextTransaction)
		if callbacks, ok := c.Get(contextAfterCommit); ok {
			for _, fn := range callbacks.([]func()) {
				buffered.beginAfterCommitCallback()
				fn()
			}
		}
		if state := auditState(c); state != nil && (buffered.Status() != state.preparedStatus || buffered.Status() >= 400) {
			// The business commit is durable, while a provider/callback may
			// still fail. Preserve both facts instead of rewriting history.
			state.recorded = false
			state.phase = "after_commit"
			finishIndependentAudit(c)
		}
		buffered.publish()
	}
}

// QueriesFromCtx returns the request-scoped queries installed by
// RequireTenantTx, or nil when the middleware did not run.
func QueriesFromCtx(c *gin.Context) (any, bool) {
	v, ok := c.Get(ContextQueries)
	return v, ok
}

// GetRole returns the authenticated user's role within the tenant.
func GetRole(c *gin.Context) string {
	role, ok := c.Get(ContextRole)
	if ok {
		if s, isStr := role.(string); isStr {
			return s
		}
	}
	return ""
}

// RequireRole allows the request only when the tenant role is one of the
// given roles. It must run after RequireTenantTx.
func RequireRole(roles ...string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		allowed[r] = struct{}{}
	}
	return func(c *gin.Context) {
		if _, ok := allowed[GetRole(c)]; !ok {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": gin.H{"code": "FORBIDDEN_ROLE", "message": "error.FORBIDDEN_ROLE"},
			})
			return
		}
		c.Next()
	}
}

package middleware

import (
	"context"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

const contextPlatformAuditCommitted = "urmail.platform_audit_committed"
const contextPlatformAuditStatus = "urmail.platform_audit_status"

// RequirePlatformTx is opt-in for platform handlers that use requestStore. It
// buffers their response and successful audit in the same business transaction.
// Do not wrap handlers that already own transactions or make provider calls.
func RequirePlatformTx(pool *db.Pool, logger *zap.Logger) gin.HandlerFunc {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(c *gin.Context) {
		if pool == nil {
			c.AbortWithStatusJSON(503, gin.H{"error": gin.H{"code": "DB_ERROR"}})
			return
		}
		tx, err := pool.PlatformTx(c.Request.Context())
		if err != nil {
			c.AbortWithStatusJSON(503, gin.H{"error": gin.H{"code": "DB_ERROR"}})
			return
		}
		defer func() { _ = tx.Rollback(context.Background()); delete(c.Keys, contextTransaction) }()
		original := c.Writer
		buffered := newResponseBuffer(original)
		c.Writer = buffered
		defer func() { c.Writer = original }()
		q := store.New(tx)
		c.Set(ContextQueries, q)
		c.Set(contextTransaction, tx)
		c.Next()
		if failed, _ := c.Get("urmail.transaction_failed"); failed == true || buffered.overflow {
			c.Writer = original
			c.AbortWithStatusJSON(500, gin.H{"error": gin.H{"code": "RESPONSE_TOO_LARGE"}})
			return
		}
		if buffered.Status() >= 400 {
			_ = tx.Rollback(context.Background())
			buffered.publish()
			return
		}
		record := platformAuditRecord(c.Request.Context(), c, q, logger)
		if _, err = q.CreateAuditLog(c.Request.Context(), record); err != nil {
			RecordAuditFailure(true)
			logger.Error("prepare platform audit", zap.Error(err))
			c.Writer = original
			c.AbortWithStatusJSON(500, gin.H{"error": gin.H{"code": "AUDIT_UNAVAILABLE"}})
			return
		}
		if err = tx.Commit(c.Request.Context()); err != nil {
			logger.Error("commit platform transaction", zap.Error(err))
			c.Writer = original
			c.AbortWithStatusJSON(500, gin.H{"error": gin.H{"code": "DB_ERROR"}})
			return
		}
		c.Set(contextPlatformAuditCommitted, true)
		c.Set(contextPlatformAuditStatus, buffered.Status())
		delete(c.Keys, ContextQueries)
		delete(c.Keys, contextTransaction)
		if callbacks, ok := c.Get(contextAfterCommit); ok {
			for _, callback := range callbacks.([]func()) {
				buffered.beginAfterCommitCallback()
				callback()
			}
		}
		buffered.publish()
	}
}

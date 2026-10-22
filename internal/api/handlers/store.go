package handlers

import (
	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
)

// requestStore returns the request-scoped queries installed by the
// RequireTenantTx middleware when they satisfy the handler's store
// interface, falling back to the store the handler was constructed with.
//
// The fallback keeps unit tests (which use fake stores and no database)
// working; in production the middleware always installs a live store bound
// to the tenant transaction, so the row-level security context applies.
func requestStore[T any](c *gin.Context, fallback T) T {
	if v, ok := middleware.QueriesFromCtx(c); ok {
		if q, ok := v.(T); ok {
			return q
		}
	}
	return fallback
}

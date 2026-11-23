package handlers

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/db/isolation"
)

// platformCtx binds the platform scope for handler statements that are
// registered platform-scoped (e.g. calendar journals, support staff, native
// receipt bookkeeping). Callers still enforce mailbox/tenant authorization
// before reaching these statements.
func platformCtx(c *gin.Context) context.Context {
	return isolation.WithScope(c.Request.Context(), isolation.ScopeContext{Scope: isolation.ScopePlatform})
}

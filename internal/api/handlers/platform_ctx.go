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

// bindPlatformScope pins the platform scope on the request context. Use it at
// the top of a trusted platform-pool entry point so every statement below
// (including helpers that receive the gin context) runs under one scope.
func bindPlatformScope(c *gin.Context) {
	c.Request = c.Request.WithContext(platformCtx(c))
}

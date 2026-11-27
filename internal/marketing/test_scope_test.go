package marketing

import (
	"context"

	"github.com/bushixiucai/urmail/internal/db/isolation"
)

// testTenantCtx binds the tenant a test itself created; tests that act as
// that tenant must carry its scope, never a platform bypass.
func testTenantCtx(tenant int64) context.Context {
	return isolation.WithScope(context.Background(), isolation.ScopeContext{Scope: isolation.ScopeTenant, TenantID: tenant})
}

// testPlatformCtx binds the platform scope for tests that exercise the
// scheduler/worker platform statements.
func testPlatformCtx() context.Context {
	return isolation.WithScope(context.Background(), isolation.ScopeContext{Scope: isolation.ScopePlatform})
}

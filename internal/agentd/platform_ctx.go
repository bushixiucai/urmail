package agentd

import (
	"context"
	"time"

	"github.com/bushixiucai/urmail/internal/db/isolation"
)

// platformContext starts a detached context for one trusted daemon entry that
// reads platform-scoped rows. The daemon runs these reads on behalf of the
// platform (render/certificate paths), so the scope is bound here rather than
// inherited from a request.
func platformContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	return isolation.WithScope(ctx, isolation.ScopeContext{Scope: isolation.ScopePlatform}), cancel
}
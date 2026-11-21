package db

import (
	"context"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/db/isolation"
)

func TestPoolScopeMappingRejectsUnsupportedScopes(t *testing.T) {
	ctx := context.Background()
	for _, scope := range []isolation.Scope{"", isolation.ScopeSystem, isolation.ScopePreAuth, "admin"} {
		if _, err := Open(ctx, "postgres://localhost/urmail", scope); err == nil {
			t.Fatalf("Open accepted unsupported scope %q", scope)
		}
		if _, err := OpenBudgeted(ctx, "postgres://localhost/urmail", scope, "bad", Budget{
			MaxConns:         1,
			MaxQueue:         0,
			AcquireTimeout:   time.Second,
			OperationTimeout: time.Second,
		}); err == nil {
			t.Fatalf("OpenBudgeted accepted unsupported scope %q", scope)
		}
	}

	var nilPool *Pool
	if nilPool.Scope() != "" || nilPool.IsPlatform() {
		t.Fatalf("nil pool reported unexpected scope=%q isPlatform=%v", nilPool.Scope(), nilPool.IsPlatform())
	}
	tenantPool := &Pool{scope: isolation.ScopeTenant}
	if tenantPool.Scope() != isolation.ScopeTenant || tenantPool.IsPlatform() {
		t.Fatalf("tenant pool reported scope=%q isPlatform=%v", tenantPool.Scope(), tenantPool.IsPlatform())
	}
	platformPool := &Pool{scope: isolation.ScopePlatform}
	if platformPool.Scope() != isolation.ScopePlatform || !platformPool.IsPlatform() {
		t.Fatalf("platform pool reported scope=%q isPlatform=%v", platformPool.Scope(), platformPool.IsPlatform())
	}
}

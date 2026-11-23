package isolation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
)

func TestScopedQuerierPanicsOnNilInputs(t *testing.T) {
	t.Run("NewScopedQuerier(nil)", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("expected NewScopedQuerier(nil) to panic")
			}
		}()
		NewScopedQuerier(nil)
	})

	t.Run("WrapQuerier(nil, nil)", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("expected WrapQuerier(nil, nil) to panic")
			}
		}()
		WrapQuerier(nil, nil)
	})
}

func parsePositionalTenantIDMethods(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "store", "querier.go"))
	if err != nil {
		t.Fatalf("read internal/store/querier.go: %v", err)
	}
	out := make(map[string]bool)
	re := regexp.MustCompile(`(?m)^\t([A-Z]\w+)\(([^)]*)\)`)
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		name := m[1]
		params := strings.Split(m[2], ",")
		if len(params) >= 2 && strings.HasPrefix(strings.TrimSpace(params[1]), "tenantID ") {
			out[name] = true
		}
	}
	return out
}

func expectedScopeError(scope Scope) error {
	switch scope {
	case ScopeTenant:
		return ErrMissingTenantScope
	case ScopePlatform:
		return ErrPlatformScopeRequired
	case ScopeSystem:
		return ErrSystemScopeRequired
	case ScopePreAuth:
		return ErrPreAuthScopeRequired
	default:
		return ErrMissingScope
	}
}

func paramTenantOverrides(t *testing.T) map[string]string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "scripts", "sqlite", "param_tenant_actor_fields.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read param overrides: %v", err)
	}
	var out map[string]string
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse param overrides: %v", err)
	}
	return out
}

func buildCallArgs(t *testing.T, m reflect.Method, ctx context.Context, positionalTID bool, tenantID int64, valid bool) ([]reflect.Value, bool) {
	t.Helper()
	mt := m.Type
	args := make([]reflect.Value, mt.NumIn())
	if ctx == nil {
		args[0] = reflect.Zero(mt.In(0))
	} else {
		args[0] = reflect.ValueOf(ctx)
	}
	hasTenantArg := false
	for i := 1; i < mt.NumIn(); i++ {
		pt := mt.In(i)
		v := reflect.New(pt).Elem()
		if pt == reflect.TypeOf(int64(0)) && i == 1 && positionalTID {
			v.SetInt(tenantID)
			hasTenantArg = true
		} else if pt == reflect.TypeOf(pgtype.Int8{}) && i == 1 && positionalTID {
			v.Set(reflect.ValueOf(pgtype.Int8{Int64: tenantID, Valid: valid}))
			hasTenantArg = true
		} else if pt.Kind() == reflect.Struct {
			if field, ok := paramTenantOverrides(t)[m.Name]; ok {
				if tf := v.FieldByName(field); tf.IsValid() && tf.Type() == reflect.TypeOf(int64(0)) {
					tf.SetInt(tenantID)
					hasTenantArg = true
				}
			}
			if tf := v.FieldByName("TenantID"); tf.IsValid() {
				switch tf.Type() {
				case reflect.TypeOf(int64(0)):
					tf.SetInt(tenantID)
					hasTenantArg = true
				case reflect.TypeOf(pgtype.Int8{}):
					tf.Set(reflect.ValueOf(pgtype.Int8{Int64: tenantID, Valid: valid}))
					hasTenantArg = true
				}
			}
		}
		args[i] = v
	}
	return args, hasTenantArg
}

func extractCallError(t *testing.T, results []reflect.Value) error {
	t.Helper()
	if len(results) == 0 {
		t.Fatal("method returned 0 values")
	}
	last := results[len(results)-1]
	if last.IsNil() {
		return nil
	}
	err, ok := last.Interface().(error)
	if !ok {
		t.Fatalf("last return value is %T, want error", last.Interface())
	}
	return err
}

func TestScopedQuerierAll702MethodsMatrix(t *testing.T) {
	querierType := reflect.TypeOf((*store.Querier)(nil)).Elem()
	const wantMethods = 702
	if querierType.NumMethod() != wantMethods {
		t.Fatalf("store.Querier has %d methods, want %d", querierType.NumMethod(), wantMethods)
	}

	scopes, err := AllMethodScopes()
	if err != nil {
		t.Fatalf("AllMethodScopes: %v", err)
	}
	positionalTID := parsePositionalTenantIDMethods(t)

	spy := &spyQuerier{}
	sq := NewScopedQuerier(spy)
	qv := reflect.ValueOf(store.Querier(sq))

	validContexts := map[Scope]context.Context{
		ScopeTenant:   WithScope(context.Background(), ScopeContext{Scope: ScopeTenant, TenantID: 101, ActorID: 7}),
		ScopePlatform: WithScope(context.Background(), ScopeContext{Scope: ScopePlatform, ActorID: 1}),
		ScopeSystem:   WithScope(context.Background(), ScopeContext{Scope: ScopeSystem}),
		ScopePreAuth:  WithScope(context.Background(), ScopeContext{Scope: ScopePreAuth}),
	}
	allScopes := []Scope{ScopeTenant, ScopePlatform, ScopeSystem, ScopePreAuth}

	var checkedTenantArgs int
	for i := 0; i < querierType.NumMethod(); i++ {
		ifMethod := querierType.Method(i)
		name := ifMethod.Name
		expected, ok := scopes[name]
		if !ok {
			t.Fatalf("store.Querier method %s missing from method_scopes.json", name)
		}

		m := qv.MethodByName(name)
		if !m.IsValid() {
			t.Fatalf("ScopedQuerier missing method %s", name)
		}
		isPosTID := positionalTID[name]
		wantErr := expectedScopeError(expected)

		// 1. Missing scope (Background & nil context) must fail closed without invoking inner store.
		for _, emptyCtx := range []context.Context{context.Background(), nil} {
			spy.calls = 0
			args, _ := buildCallArgs(t, ifMethod, emptyCtx, isPosTID, 101, true)
			gotErr := extractCallError(t, m.Call(args))
			if !errors.Is(gotErr, ErrMissingScope) || !errors.Is(gotErr, wantErr) {
				t.Fatalf("%s with empty ctx (%v): err=%v, want ErrMissingScope + %v", name, emptyCtx, gotErr, wantErr)
			}
			if spy.calls != 0 {
				t.Fatalf("%s invoked inner store on empty ctx", name)
			}
		}

		// 2. Every other scope must fail closed without invoking inner store.
		for _, wrongScope := range allScopes {
			if wrongScope == expected {
				continue
			}
			spy.calls = 0
			args, _ := buildCallArgs(t, ifMethod, validContexts[wrongScope], isPosTID, 101, true)
			gotErr := extractCallError(t, m.Call(args))
			if !errors.Is(gotErr, ErrMissingScope) || !errors.Is(gotErr, wantErr) {
				t.Fatalf("%s with wrong scope %s: err=%v, want ErrMissingScope + %v", name, wrongScope, gotErr, wantErr)
			}
			if spy.calls != 0 {
				t.Fatalf("%s invoked inner store on wrong scope %s", name, wrongScope)
			}
		}

		// 3. Tenant methods require positive TenantID and reject cross-tenant parameters.
		if expected == ScopeTenant {
			for _, badTID := range []int64{0, -1} {
				spy.calls = 0
				badCtx := WithScope(context.Background(), ScopeContext{Scope: ScopeTenant, TenantID: badTID})
				args, _ := buildCallArgs(t, ifMethod, badCtx, isPosTID, 101, true)
				gotErr := extractCallError(t, m.Call(args))
				if !errors.Is(gotErr, ErrMissingTenantScope) {
					t.Fatalf("%s with tenant_id=%d: err=%v, want ErrMissingTenantScope", name, badTID, gotErr)
				}
				if spy.calls != 0 {
					t.Fatalf("%s invoked inner store with non-positive tenant_id=%d", name, badTID)
				}
			}

			spy.calls = 0
			mismatchArgs, hasTenantArg := buildCallArgs(t, ifMethod, validContexts[ScopeTenant], isPosTID, 202, true)
			if hasTenantArg {
				checkedTenantArgs++
				gotErr := extractCallError(t, m.Call(mismatchArgs))
				if !errors.Is(gotErr, ErrCrossTenantAccess) {
					t.Fatalf("%s with cross-tenant arg (202 != 101): err=%v, want ErrCrossTenantAccess", name, gotErr)
				}
				if spy.calls != 0 {
					t.Fatalf("%s invoked inner store on cross-tenant parameter", name)
				}

				// If TenantID uses pgtype.Int8, Invalid (NULL) must also be rejected.
				if ifMethod.Type.NumIn() >= 2 {
					pt := ifMethod.Type.In(1)
					isPgInt8 := (pt == reflect.TypeOf(pgtype.Int8{}) && isPosTID)
					if !isPgInt8 && pt.Kind() == reflect.Struct {
						if tf, ok := pt.FieldByName("TenantID"); ok && tf.Type == reflect.TypeOf(pgtype.Int8{}) {
							isPgInt8 = true
						}
					}
					if isPgInt8 {
						spy.calls = 0
						nullArgs, _ := buildCallArgs(t, ifMethod, validContexts[ScopeTenant], isPosTID, 101, false)
						gotErr := extractCallError(t, m.Call(nullArgs))
						if !errors.Is(gotErr, ErrCrossTenantAccess) {
							t.Fatalf("%s with invalid pgtype.Int8 TenantID: err=%v, want ErrCrossTenantAccess", name, gotErr)
						}
						if spy.calls != 0 {
							t.Fatalf("%s invoked inner store on invalid pgtype.Int8 TenantID", name)
						}
					}
				}
			}
		}

		// 3b. Explicit param-field tenant overrides (e.g. settings ScopeID)
		// are refused when they name another tenant.
		if field, ok := paramTenantOverrides(t)[name]; ok && expected == ScopeTenant {
			badArgs, _ := buildCallArgs(t, ifMethod, validContexts[ScopeTenant], isPosTID, 202, true)
			if fm, ok := badArgs[1].FieldByName(field).Interface().(int64); !ok || fm != 202 {
				t.Fatalf("%s override field %s not populated", name, field)
			}
			spy.calls = 0
			if gotErr := extractCallError(t, m.Call(badArgs)); !errors.Is(gotErr, ErrCrossTenantAccess) {
				t.Fatalf("%s with foreign %s: err=%v, want ErrCrossTenantAccess", name, field, gotErr)
			}
			if spy.calls != 0 {
				t.Fatalf("%s invoked inner store on foreign %s", name, field)
			}
			checkedTenantArgs++
		}

		// 4. Matching scope & matching tenant delegates cleanly to inner store.
		spy.calls = 0
		spy.lastMethod = ""
		okArgs, _ := buildCallArgs(t, ifMethod, validContexts[expected], isPosTID, 101, true)
		if gotErr := extractCallError(t, m.Call(okArgs)); gotErr != nil {
			t.Fatalf("%s with valid scope %s failed: %v", name, expected, gotErr)
		}
		if spy.calls != 1 || spy.lastMethod != name {
			t.Fatalf("%s valid delegation got calls=%d lastMethod=%q", name, spy.calls, spy.lastMethod)
		}
	}

	if checkedTenantArgs == 0 {
		t.Fatal("expected non-zero tenant methods with explicit TenantID checks")
	}
}

func TestScopedQuerierCriticalPlatformAndCrossTenantMethods(t *testing.T) {
	spy := &spyQuerier{}
	q := WrapQuerier(nil, spy)

	tenant10 := WithScope(context.Background(), ScopeContext{Scope: ScopeTenant, TenantID: 10, ActorID: 55})
	platformCtx := WithScope(context.Background(), ScopeContext{Scope: ScopePlatform, ActorID: 1})
	systemCtx := WithScope(context.Background(), ScopeContext{Scope: ScopeSystem})

	// Critical reclassified session lifecycle methods must reject tenant contexts.
	for _, tc := range []struct {
		name string
		call func(context.Context) error
	}{
		{"DeleteSession", func(ctx context.Context) error { return q.DeleteSession(ctx, "sid-1") }},
		{"MarkSessionMFAVerified", func(ctx context.Context) error { return q.MarkSessionMFAVerified(ctx, "sid-1") }},
		{"DeleteOtherUserSessions", func(ctx context.Context) error {
			return q.DeleteOtherUserSessions(ctx, store.DeleteOtherUserSessionsParams{UserID: 55, ID: "sid-1"})
		}},
		{"DeleteUserSession", func(ctx context.Context) error {
			_, err := q.DeleteUserSession(ctx, store.DeleteUserSessionParams{UserID: 55, ID: "sid-1"})
			return err
		}},
		{"MarkSessionLegacyOnly", func(ctx context.Context) error { return q.MarkSessionLegacyOnly(ctx, "sid-1") }},
	} {
		spy.calls = 0
		if err := tc.call(tenant10); !errors.Is(err, ErrPlatformScopeRequired) {
			t.Fatalf("%s allowed tenant ctx: err=%v", tc.name, err)
		}
		if spy.calls != 0 {
			t.Fatalf("%s called inner store on rejected tenant ctx", tc.name)
		}
		if err := tc.call(platformCtx); err != nil {
			t.Fatalf("%s failed under platform ctx: %v", tc.name, err)
		}
		if spy.calls != 1 {
			t.Fatalf("%s did not delegate under platform ctx", tc.name)
		}
	}

	// Reclassified auth.sql & audit.sql platform methods must reject tenant contexts.
	for _, tc := range []struct {
		name string
		call func(context.Context) error
	}{
		{"GetUserTenants", func(ctx context.Context) error { _, err := q.GetUserTenants(ctx, 1); return err }},
		{"CreateSession", func(ctx context.Context) error {
			_, err := q.CreateSession(ctx, store.CreateSessionParams{ID: "sid-1", UserID: 1})
			return err
		}},
		{"GetSessionByID", func(ctx context.Context) error { _, err := q.GetSessionByID(ctx, "sid-1"); return err }},
		{"UpdateSessionTenant", func(ctx context.Context) error {
			return q.UpdateSessionTenant(ctx, store.UpdateSessionTenantParams{ID: "sid-1", TenantID: pgtype.Int8{Int64: 10, Valid: true}})
		}},
		{"ListSessionsForUser", func(ctx context.Context) error { _, err := q.ListSessionsForUser(ctx, 1); return err }},
		{"CreateAuditLog", func(ctx context.Context) error {
			_, err := q.CreateAuditLog(ctx, store.CreateAuditLogParams{Action: "platform.op"})
			return err
		}},
	} {
		spy.calls = 0
		if err := tc.call(tenant10); !errors.Is(err, ErrPlatformScopeRequired) {
			t.Fatalf("%s allowed tenant ctx: err=%v", tc.name, err)
		}
		if spy.calls != 0 {
			t.Fatalf("%s called inner store on rejected tenant ctx", tc.name)
		}
		if err := tc.call(platformCtx); err != nil {
			t.Fatalf("%s failed under platform ctx: %v", tc.name, err)
		}
		if spy.calls != 1 {
			t.Fatalf("%s did not delegate under platform ctx", tc.name)
		}
	}

	// System-scoped cleanup rejects tenant & platform scopes.
	spy.calls = 0
	if err := q.DeleteExpiredSessions(tenant10); !errors.Is(err, ErrSystemScopeRequired) {
		t.Fatalf("DeleteExpiredSessions allowed tenant ctx: %v", err)
	}
	if err := q.DeleteExpiredSessions(platformCtx); !errors.Is(err, ErrSystemScopeRequired) {
		t.Fatalf("DeleteExpiredSessions allowed platform ctx: %v", err)
	}
	if err := q.DeleteExpiredSessions(systemCtx); err != nil {
		t.Fatalf("DeleteExpiredSessions failed under system ctx: %v", err)
	}

	// Cross-tenant positional, struct int64, and struct pgtype.Int8 parameters all reject mismatch.
	spy.calls = 0
	if _, err := q.ListAliasesByTenant(tenant10, 20); !errors.Is(err, ErrCrossTenantAccess) {
		t.Fatalf("ListAliasesByTenant cross-tenant err=%v", err)
	}
	if _, err := q.GetMailboxByID(tenant10, store.GetMailboxByIDParams{TenantID: 20, ID: 1}); !errors.Is(err, ErrCrossTenantAccess) {
		t.Fatalf("GetMailboxByID cross-tenant err=%v", err)
	}
	if _, err := q.ListDeliveryEventsByTenant(tenant10, pgtype.Int8{Int64: 20, Valid: true}); !errors.Is(err, ErrCrossTenantAccess) {
		t.Fatalf("ListDeliveryEventsByTenant cross-tenant err=%v", err)
	}
	if _, err := q.ListAuditLogsByTenant(tenant10, store.ListAuditLogsByTenantParams{TenantID: pgtype.Int8{Int64: 20, Valid: true}, Limit: 10}); !errors.Is(err, ErrCrossTenantAccess) {
		t.Fatalf("ListAuditLogsByTenant cross-tenant err=%v", err)
	}
	if _, err := q.ListAuditLogsByTenant(tenant10, store.ListAuditLogsByTenantParams{TenantID: pgtype.Int8{Valid: false}, Limit: 10}); !errors.Is(err, ErrCrossTenantAccess) {
		t.Fatalf("ListAuditLogsByTenant null tenant err=%v", err)
	}
	if spy.calls != 0 {
		t.Fatalf("cross-tenant calls leaked to inner store: calls=%d", spy.calls)
	}
}

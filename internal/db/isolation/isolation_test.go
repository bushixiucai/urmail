package isolation

import (
	"context"
	"errors"
	"testing"
)

func TestMethodScopesRegistryAndCheckContext(t *testing.T) {
	count, err := RegisteredMethods()
	if err != nil {
		t.Fatalf("load method_scopes.json: %v", err)
	}
	if count == 0 {
		t.Fatal("expected non-empty method_scopes.json")
	}

	// Tenant method requires positive tenant_id in tenant scope.
	if err := CheckContext(context.Background(), "ListAliasesByTenant"); !errors.Is(err, ErrMissingScope) {
		t.Fatalf("expected ErrMissingScope without context, got %v", err)
	}
	if err := CheckContext(WithScope(context.Background(), ScopeContext{Scope: ScopePlatform}), "ListAliasesByTenant"); !errors.Is(err, ErrMissingScope) {
		t.Fatalf("expected ErrMissingScope for platform scope on tenant method, got %v", err)
	}
	if err := CheckContext(WithScope(context.Background(), ScopeContext{Scope: ScopeTenant, TenantID: 0}), "ListAliasesByTenant"); !errors.Is(err, ErrMissingScope) {
		t.Fatalf("expected ErrMissingScope for zero tenant_id, got %v", err)
	}
	if err := CheckContext(WithScope(context.Background(), ScopeContext{Scope: ScopeTenant, TenantID: 42}), "ListAliasesByTenant"); err != nil {
		t.Fatalf("valid tenant scope rejected: %v", err)
	}

	// Platform method requires platform scope and rejects tenant scope.
	if err := CheckContext(WithScope(context.Background(), ScopeContext{Scope: ScopeTenant, TenantID: 42}), "PlatformListTenants"); !errors.Is(err, ErrMissingScope) {
		t.Fatalf("expected ErrMissingScope for tenant scope on platform method, got %v", err)
	}
	if err := CheckContext(WithScope(context.Background(), ScopeContext{Scope: ScopePlatform}), "PlatformListTenants"); err != nil {
		t.Fatalf("valid platform scope rejected: %v", err)
	}

	// Unregistered method fails closed.
	if err := CheckContext(WithScope(context.Background(), ScopeContext{Scope: ScopePlatform}), "UnregisteredMethodXYZ"); !errors.Is(err, ErrMissingScope) {
		t.Fatalf("expected ErrMissingScope for unregistered method, got %v", err)
	}
}

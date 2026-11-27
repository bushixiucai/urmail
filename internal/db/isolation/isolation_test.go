package isolation

import (
	"context"
	"encoding/json"
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

	var raw struct {
		TenantIDUnbound []string `json:"tenant_id_unbound"`
	}
	if err := json.Unmarshal(methodScopesJSON, &raw); err != nil {
		t.Fatalf("unmarshal method_scopes.json: %v", err)
	}
	if len(raw.TenantIDUnbound) != 0 {
		t.Fatalf("expected 0 tenant_id_unbound methods, got %v", raw.TenantIDUnbound)
	}

	// Reclassified session lifecycle methods (S2 tenant_id_unbound closure).
	if s, ok, err := MethodScope("DeleteExpiredSessions"); err != nil || !ok || s != ScopeSystem {
		t.Fatalf("DeleteExpiredSessions scope = (%q, %v, %v), want system", s, ok, err)
	}
	for _, m := range []string{
		"DeleteSession",
		"DeleteOtherUserSessions",
		"DeleteUserSession",
		"MarkSessionMFAVerified",
		"MarkSessionLegacyOnly",
	} {
		if s, ok, err := MethodScope(m); err != nil || !ok || s != ScopePlatform {
			t.Fatalf("%s scope = (%q, %v, %v), want platform", m, s, ok, err)
		}
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

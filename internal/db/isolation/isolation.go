// Package isolation defines the S2/S3 method-scope registry and decorator
// seam attached to db.Queries and db.Querier (v3.2 §2.1 & §7 S3).
//
// scripts/sqlite/method_scopes.json is mirrored here as an embedded registry so
// runtime and compile-time checks share one authoritative map of every
// store.Querier method to its required scope (tenant, platform, system,
// pre-auth). ScopedQuerier wraps store.Querier across all 703 methods at
// db.Querier, and DBTXScopeHook enforces context scope across *store.Queries
// and (*store.Queries).WithTx at db.Queries.
package isolation

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sync"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
)

// Scope identifies the authorization boundary required by a sqlc method.
type Scope string

// Scope constants identify the trusted context a call runs under.
const (
	ScopeTenant   Scope = "tenant"
	ScopePlatform Scope = "platform"
	ScopeSystem   Scope = "system"
	ScopePreAuth  Scope = "pre-auth"
)

var (
	// ErrMissingScope is the base error returned when a method executes without a
	// matching scope in the caller context.
	ErrMissingScope = errors.New("db isolation: missing or mismatched scope context")

	// ErrMissingTenantScope is returned when a tenant-scoped method executes
	// without a valid positive TenantID in ScopeTenant context.
	ErrMissingTenantScope = errors.New("db isolation: missing or invalid tenant scope")

	// ErrCrossTenantAccess is returned when a tenant-scoped method receives a
	// TenantID parameter that differs from the authenticated context TenantID.
	ErrCrossTenantAccess = errors.New("db isolation: cross-tenant access denied")

	// ErrPlatformScopeRequired is returned when a platform-scoped method is
	// invoked outside ScopePlatform (including from a tenant context).
	ErrPlatformScopeRequired = errors.New("db isolation: platform scope required")

	// ErrSystemScopeRequired is returned when a system-scoped method is invoked
	// outside ScopeSystem.
	ErrSystemScopeRequired = errors.New("db isolation: system scope required")

	// ErrPreAuthScopeRequired is returned when a pre-auth-scoped method is
	// invoked outside ScopePreAuth.
	ErrPreAuthScopeRequired = errors.New("db isolation: pre-auth scope required")
)

type scopeContextKey struct{}

// ScopeContext carries the caller's validated database execution boundary.
type ScopeContext struct {
	Scope    Scope
	TenantID int64
	ActorID  int64
}

// WithScope attaches a ScopeContext to ctx.
func WithScope(ctx context.Context, sc ScopeContext) context.Context {
	return context.WithValue(ctx, scopeContextKey{}, sc)
}

// ScopeFromContext extracts the ScopeContext from ctx if present.
func ScopeFromContext(ctx context.Context) (ScopeContext, bool) {
	if ctx == nil {
		return ScopeContext{}, false
	}
	sc, ok := ctx.Value(scopeContextKey{}).(ScopeContext)
	return sc, ok
}

//go:embed method_scopes.json
var methodScopesJSON []byte

var (
	registryOnce sync.Once
	registryMap  map[string]Scope
	registryErr  error
)

type methodScopesPayload struct {
	Scopes map[string]string `json:"scopes"`
}

func loadRegistry() (map[string]Scope, error) {
	registryOnce.Do(func() {
		var parsed methodScopesPayload
		if err := json.Unmarshal(methodScopesJSON, &parsed); err != nil {
			registryErr = fmt.Errorf("parse embedded method_scopes.json: %w", err)
			return
		}
		m := make(map[string]Scope, len(parsed.Scopes))
		for name, raw := range parsed.Scopes {
			s := Scope(raw)
			switch s {
			case ScopeTenant, ScopePlatform, ScopeSystem, ScopePreAuth:
				m[name] = s
			default:
				registryErr = fmt.Errorf("method %s has unknown scope %q", name, raw)
				return
			}
		}
		registryMap = m
	})
	return registryMap, registryErr
}

// MethodScope looks up the declared scope for a sqlc method name.
func MethodScope(method string) (Scope, bool, error) {
	reg, err := loadRegistry()
	if err != nil {
		return "", false, err
	}
	s, ok := reg[method]
	return s, ok, nil
}

// RegisteredMethods returns the count of registered methods in method_scopes.json.
func RegisteredMethods() (int, error) {
	reg, err := loadRegistry()
	if err != nil {
		return 0, err
	}
	return len(reg), nil
}

// AllMethodScopes returns a copy of the registered method -> scope map.
func AllMethodScopes() (map[string]Scope, error) {
	reg, err := loadRegistry()
	if err != nil {
		return nil, err
	}
	out := make(map[string]Scope, len(reg))
	maps.Copy(out, reg)
	return out, nil
}

func scopeMismatchError(method string, expected Scope, got *Scope) error {
	var specific error
	switch expected {
	case ScopeTenant:
		specific = ErrMissingTenantScope
	case ScopePlatform:
		specific = ErrPlatformScopeRequired
	case ScopeSystem:
		specific = ErrSystemScopeRequired
	case ScopePreAuth:
		specific = ErrPreAuthScopeRequired
	default:
		return fmt.Errorf("%w: method %s has unsupported scope %q", ErrMissingScope, method, expected)
	}
	if got == nil {
		if expected == ScopeTenant {
			return fmt.Errorf("%w: %w: method %s requires positive tenant_id in tenant scope", specific, ErrMissingScope, method)
		}
		return fmt.Errorf("%w: %w: method %s requires %s scope", specific, ErrMissingScope, method, expected)
	}
	if expected == ScopeTenant {
		return fmt.Errorf("%w: %w: method %s requires positive tenant_id in tenant scope (got scope=%s)", specific, ErrMissingScope, method, *got)
	}
	return fmt.Errorf("%w: %w: method %s requires %s scope, got %s", specific, ErrMissingScope, method, expected, *got)
}

func validateScopeContext(ctx context.Context, method string, expected Scope) (ScopeContext, error) {
	sc, has := ScopeFromContext(ctx)
	if !has {
		return ScopeContext{}, scopeMismatchError(method, expected, nil)
	}
	if expected == ScopeTenant {
		if sc.Scope != ScopeTenant || sc.TenantID <= 0 {
			got := sc.Scope
			return ScopeContext{}, scopeMismatchError(method, expected, &got)
		}
		return sc, nil
	}
	if sc.Scope != expected {
		got := sc.Scope
		return ScopeContext{}, scopeMismatchError(method, expected, &got)
	}
	return sc, nil
}

// CheckContext verifies that ctx holds a ScopeContext compatible with method's
// declared scope in method_scopes.json. Unknown methods fail closed.
func CheckContext(ctx context.Context, method string) error {
	expected, ok, err := MethodScope(method)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: unregistered method %q", ErrMissingScope, method)
	}
	_, err = validateScopeContext(ctx, method, expected)
	return err
}

// CheckMethod verifies that method is registered in method_scopes.json with
// the compile-time expected scope and that ctx holds a matching ScopeContext.
func CheckMethod(ctx context.Context, method string, compiled Scope) (ScopeContext, error) {
	expected, ok, err := MethodScope(method)
	if err != nil {
		return ScopeContext{}, err
	}
	if !ok {
		return ScopeContext{}, fmt.Errorf("%w: unregistered method %q", ErrMissingScope, method)
	}
	if expected != compiled {
		return ScopeContext{}, fmt.Errorf("%w: method %s scope drift (compiled=%s registry=%s)", ErrMissingScope, method, compiled, expected)
	}
	return validateScopeContext(ctx, method, expected)
}

// CheckTenantMatch verifies that a tenant-scoped method's explicit TenantID
// argument matches the authenticated ScopeContext TenantID.
func CheckTenantMatch(sc ScopeContext, method string, paramTenantID int64) error {
	if sc.Scope != ScopeTenant || sc.TenantID <= 0 {
		return fmt.Errorf("%w: %w: method %s requires positive tenant_id in tenant scope", ErrMissingTenantScope, ErrMissingScope, method)
	}
	if paramTenantID != sc.TenantID {
		return fmt.Errorf("%w: method %s param tenant_id=%d != context tenant_id=%d", ErrCrossTenantAccess, method, paramTenantID, sc.TenantID)
	}
	return nil
}

// CheckTenantMatchInt8 verifies that a tenant-scoped method's nullable
// pgtype.Int8 TenantID argument is valid and equals the authenticated TenantID.
func CheckTenantMatchInt8(sc ScopeContext, method string, paramTenantID pgtype.Int8) error {
	if sc.Scope != ScopeTenant || sc.TenantID <= 0 {
		return fmt.Errorf("%w: %w: method %s requires positive tenant_id in tenant scope", ErrMissingTenantScope, ErrMissingScope, method)
	}
	if !paramTenantID.Valid || paramTenantID.Int64 != sc.TenantID {
		return fmt.Errorf("%w: method %s param tenant_id=(valid=%v,%d) != context tenant_id=%d", ErrCrossTenantAccess, method, paramTenantID.Valid, paramTenantID.Int64, sc.TenantID)
	}
	return nil
}

// Hook is the S2/S3 assembly seam invoked by db.Queries(tx). S3 populates a
// scope-enforcing DBTX decorator here without modifying callers.
type Hook func(tx store.DBTX, q *store.Queries) *store.Queries

// QuerierHook is the interface-level seam for S3 decorators wrapping store.Querier.
type QuerierHook func(tx store.DBTX, q store.Querier) store.Querier

// ScopedQuerier is the S3 scope-enforcing store.Querier decorator covering all
// 703 methods in internal/store/querier.go.
type ScopedQuerier struct {
	inner store.Querier
}

var _ store.Querier = (*ScopedQuerier)(nil)

// NewScopedQuerier wraps inner with S3 method-level scope and tenant-id
// enforcement. Nil inputs panic immediately to fail closed.
func NewScopedQuerier(inner store.Querier) *ScopedQuerier {
	if inner == nil {
		panic("isolation.NewScopedQuerier: nil inner store.Querier")
	}
	return &ScopedQuerier{inner: inner}
}

// WrapQuerier implements QuerierHook by wrapping q in ScopedQuerier.
func WrapQuerier(_ store.DBTX, q store.Querier) store.Querier {
	return NewScopedQuerier(q)
}

// Package isolation defines the S2/S3 method-scope registry and decorator
// seam attached to db.Queries (v3.2 §2.1).
//
// In S2, scripts/sqlite/method_scopes.json is mirrored here as an embedded
// registry so runtime and compile-time checks share one authoritative map of
// every sqlc method to its required scope (tenant, platform, system, pre-auth).
// S3 wraps *store.Queries at db.Queries with full method-level enforcement.
package isolation

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/bushixiucai/urmail/internal/store"
)

// Scope identifies the authorization boundary required by a sqlc method.
type Scope string

const (
	ScopeTenant   Scope = "tenant"
	ScopePlatform Scope = "platform"
	ScopeSystem   Scope = "system"
	ScopePreAuth  Scope = "pre-auth"
)

// ErrMissingScope is returned when a method executes without a matching scope
// in the caller context.
var ErrMissingScope = errors.New("db isolation: missing or mismatched scope context")

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
	sc, has := ScopeFromContext(ctx)
	if !has {
		return fmt.Errorf("%w: method %s requires %s scope", ErrMissingScope, method, expected)
	}
	if expected == ScopeTenant {
		if sc.Scope != ScopeTenant || sc.TenantID <= 0 {
			return fmt.Errorf("%w: method %s requires positive tenant_id in tenant scope", ErrMissingScope, method)
		}
		return nil
	}
	if sc.Scope != expected {
		return fmt.Errorf("%w: method %s requires %s scope, got %s", ErrMissingScope, method, expected, sc.Scope)
	}
	return nil
}

// Hook is the S2/S3 assembly seam invoked by db.Queries(tx). S3 populates a
// generated WithTenant decorator here without modifying callers.
type Hook func(tx store.DBTX, q *store.Queries) *store.Queries

// QuerierHook is the interface-level seam for S3 decorators wrapping store.Querier.
type QuerierHook func(tx store.DBTX, q store.Querier) store.Querier

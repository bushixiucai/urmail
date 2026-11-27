package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/store"
)

type tenantCurrentStub struct{ err error }

func (s tenantCurrentStub) GetTenantByID(context.Context, int64) (store.Tenants, error) {
	if s.err != nil {
		return store.Tenants{}, s.err
	}
	return store.Tenants{ID: 1, Name: "Default", Slug: "default", Status: "active"}, nil
}
func (tenantCurrentStub) ListSettingsByScopes(context.Context, store.ListSettingsByScopesParams) ([]store.Settings, error) {
	return nil, nil
}
func (tenantCurrentStub) CountDomainsByTenant(context.Context, int64) (int64, error)   { return 0, nil }
func (tenantCurrentStub) CountMailboxesByTenant(context.Context, int64) (int64, error) { return 0, nil }

type tenantPlatformStub struct{}

func (tenantPlatformStub) PlatformGetBrandThemeByTenant(context.Context, int64) (store.PlatformGetBrandThemeByTenantRow, error) {
	return store.PlatformGetBrandThemeByTenantRow{}, pgx.ErrNoRows
}

func callTenantCurrent(t *testing.T, err error) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/tenants/current", nil)
	c.Set(middleware.ContextTenantID, int64(1))
	h := NewTenantHandler(tenantCurrentStub{err: err}, tenantPlatformStub{})
	h.Current(c)
	return rec
}

// TestTenantCurrentNeverDisguisesScopeErrors keeps a permission/isolation
// failure from surfacing as 404 TENANT_NOT_FOUND.
func TestTenantCurrentNeverDisguisesScopeErrors(t *testing.T) {
	if rec := callTenantCurrent(t, isolation.ErrPlatformScopeRequired); rec.Code != http.StatusInternalServerError {
		t.Fatalf("scope error mapped to %d, want 500", rec.Code)
	}
	if rec := callTenantCurrent(t, errors.New("driver exploded")); rec.Code != http.StatusInternalServerError {
		t.Fatalf("driver error mapped to %d, want 500", rec.Code)
	}
	if rec := callTenantCurrent(t, pgx.ErrNoRows); rec.Code != http.StatusNotFound {
		t.Fatalf("no rows mapped to %d, want 404", rec.Code)
	}
	if rec := callTenantCurrent(t, nil); rec.Code != http.StatusOK {
		t.Fatalf("valid tenant mapped to %d, want 200", rec.Code)
	}
}

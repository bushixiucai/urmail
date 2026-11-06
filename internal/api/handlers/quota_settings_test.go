package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/store"
)

type mockQuotaSettingsStore struct {
	domains          int64
	mailboxes        int64
	tenantSettings   map[string]json.RawMessage
	platformSettings map[string]json.RawMessage
}

func (m *mockQuotaSettingsStore) CountDomainsByTenant(_ context.Context, _ int64) (int64, error) {
	return m.domains, nil
}

func (m *mockQuotaSettingsStore) CountMailboxesByTenant(_ context.Context, _ int64) (int64, error) {
	return m.mailboxes, nil
}

func (m *mockQuotaSettingsStore) GetTenantSetting(_ context.Context, arg store.GetTenantSettingParams) (store.Settings, error) {
	if v, ok := m.tenantSettings[arg.Key]; ok {
		return store.Settings{Scope: "tenant", ScopeID: arg.ScopeID, Key: arg.Key, Value: v}, nil
	}
	return store.Settings{}, errors.New("not found")
}

func (m *mockQuotaSettingsStore) PlatformGetSetting(_ context.Context, key string) (store.PlatformGetSettingRow, error) {
	if v, ok := m.platformSettings[key]; ok {
		return store.PlatformGetSettingRow{Value: v}, nil
	}
	return store.PlatformGetSettingRow{}, errors.New("not found")
}

func TestQuotaCheckDomainDefaultUnlimited(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	store := &mockQuotaSettingsStore{domains: 100}

	if !quotaCheck(c, store, 1, "domain") {
		t.Fatal("expected default unlimited (-1) to allow domain creation")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
}

func TestQuotaCheckDomainPlatformLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &mockQuotaSettingsStore{
		domains:          5,
		platformSettings: map[string]json.RawMessage{"mailbox_domain_limit": json.RawMessage("5")},
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	if quotaCheck(c, store, 1, "domain") {
		t.Fatal("expected quotaCheck to fail when domains >= limit")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", w.Code)
	}

	// Under limit should succeed
	store.domains = 4
	wUnder := httptest.NewRecorder()
	cUnder, _ := gin.CreateTestContext(wUnder)
	if !quotaCheck(cUnder, store, 1, "domain") {
		t.Fatal("expected quotaCheck to succeed when domains < limit")
	}
}

func TestQuotaCheckTenantOverrideTakesPrecedence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// Platform limit is 2, but tenant override is 10. Tenant with 5 domains should succeed.
	store := &mockQuotaSettingsStore{
		domains:          5,
		platformSettings: map[string]json.RawMessage{"mailbox_domain_limit": json.RawMessage("2")},
		tenantSettings:   map[string]json.RawMessage{"mailbox_domain_limit": json.RawMessage("10")},
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	if !quotaCheck(c, store, 1, "domain") {
		t.Fatal("expected tenant override (10) to take precedence over platform (2)")
	}

	// Conversely: platform limit is 10, tenant override is 3. Tenant with 3 domains should fail.
	store.platformSettings["mailbox_domain_limit"] = json.RawMessage("10")
	store.tenantSettings["mailbox_domain_limit"] = json.RawMessage("3")
	store.domains = 3

	wStrict := httptest.NewRecorder()
	cStrict, _ := gin.CreateTestContext(wStrict)
	if quotaCheck(cStrict, store, 1, "domain") {
		t.Fatal("expected tenant override (3) to be enforced over platform default (10)")
	}
	if wStrict.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", wStrict.Code)
	}
}

func TestQuotaCheckMailboxLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &mockQuotaSettingsStore{
		mailboxes:        20,
		platformSettings: map[string]json.RawMessage{"mailbox_limit": json.RawMessage("20")},
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	if quotaCheck(c, store, 1, "mailbox") {
		t.Fatal("expected quotaCheck to fail when mailboxes >= limit")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", w.Code)
	}

	store.mailboxes = 19
	wUnder := httptest.NewRecorder()
	cUnder, _ := gin.CreateTestContext(wUnder)
	if !quotaCheck(cUnder, store, 1, "mailbox") {
		t.Fatal("expected quotaCheck to pass when mailboxes < limit")
	}
}

func TestValidateSettingsQuotaKeys(t *testing.T) {
	validCases := []map[string]json.RawMessage{
		{"mailbox_domain_limit": json.RawMessage("10")},
		{"mailbox_domain_limit": json.RawMessage("-1")},
		{"mailbox_domain_limit": json.RawMessage("0")},
		{"mailbox_limit": json.RawMessage("100")},
		{"mailbox_limit": json.RawMessage("-1")},
		{"mailbox_quota_bytes": json.RawMessage("10737418240")},
		{"mailbox_quota_bytes": json.RawMessage("-1")},
		{"ai_default_call_limit": json.RawMessage("50")},
		{"ai_default_call_limit": json.RawMessage("0")},
		{"ai_default_call_limit": json.RawMessage("null")},
	}
	for i, c := range validCases {
		if !validateSettings(c) {
			t.Fatalf("expected valid settings for case %d: %v", i, c)
		}
	}

	invalidCases := []map[string]json.RawMessage{
		{"mailbox_domain_limit": json.RawMessage("-2")},
		{"mailbox_limit": json.RawMessage("-5")},
		{"mailbox_quota_bytes": json.RawMessage("-10")},
		{"ai_default_call_limit": json.RawMessage("-1")},
		{"ai_default_call_limit": json.RawMessage(`"not_a_number"`)},
	}
	for i, c := range invalidCases {
		if validateSettings(c) {
			t.Fatalf("expected invalid settings for case %d: %v", i, c)
		}
	}
}

func TestTenantCurrentNoSettingsNoSubscriptionDefaultUnlimited(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &shapeFakeTenantStore{
		tenant: store.Tenants{
			ID: 1, BrandID: 2, Name: "NoSettingsTenant", Slug: "nosettings", Status: "active",
		},
		settings: []store.Settings{},
	}
	h := NewTenantHandler(fake, nil)
	r := newShapeRouter(1, func(g *gin.RouterGroup) { g.GET("/tenants/current", h.Current) })

	code, raw, body := shapeRequest(t, r, http.MethodGet, "/api/v1/tenants/current", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d body %s", code, raw)
	}
	usage := shapeObject(t, body, "usage")
	if usage["domains_limit"].(float64) != -1 {
		t.Fatalf("expected domains_limit = -1 without settings/subscription, got %v", usage["domains_limit"])
	}
	if usage["mailboxes_limit"].(float64) != -1 {
		t.Fatalf("expected mailboxes_limit = -1 without settings/subscription, got %v", usage["mailboxes_limit"])
	}
	if usage["storage_bytes_limit"].(float64) != -1 {
		t.Fatalf("expected storage_bytes_limit = -1 without settings/subscription, got %v", usage["storage_bytes_limit"])
	}
}

func TestTenantCurrentPlatformDefaultAndTenantOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &shapeFakeTenantStore{
		tenant: store.Tenants{
			ID: 1, BrandID: 2, Name: "OverrideTenant", Slug: "overridetenant", Status: "active",
		},
		settings: []store.Settings{
			{Scope: "platform", ScopeID: 0, Key: "mailbox_domain_limit", Value: []byte(`5`)},
			{Scope: "platform", ScopeID: 0, Key: "mailbox_limit", Value: []byte(`20`)},
			{Scope: "platform", ScopeID: 0, Key: "mailbox_quota_bytes", Value: []byte(`1073741824`)},
			{Scope: "tenant", ScopeID: 1, Key: "mailbox_domain_limit", Value: []byte(`10`)},
		},
	}
	h := NewTenantHandler(fake, nil)
	r := newShapeRouter(1, func(g *gin.RouterGroup) { g.GET("/tenants/current", h.Current) })

	code, raw, body := shapeRequest(t, r, http.MethodGet, "/api/v1/tenants/current", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d body %s", code, raw)
	}
	usage := shapeObject(t, body, "usage")
	if usage["domains_limit"].(float64) != 10 {
		t.Fatalf("expected domains_limit = 10 from tenant override, got %v", usage["domains_limit"])
	}
	if usage["mailboxes_limit"].(float64) != 20 {
		t.Fatalf("expected mailboxes_limit = 20 from platform default, got %v", usage["mailboxes_limit"])
	}
	if usage["storage_bytes_limit"].(float64) != 1073741824 {
		t.Fatalf("expected storage_bytes_limit = 1073741824, got %v", usage["storage_bytes_limit"])
	}

	quotaStore := &mockQuotaSettingsStore{
		domains: 5,
		platformSettings: map[string]json.RawMessage{
			"mailbox_domain_limit": json.RawMessage("5"),
			"mailbox_limit":        json.RawMessage("20"),
		},
		tenantSettings: map[string]json.RawMessage{
			"mailbox_domain_limit": json.RawMessage("10"),
		},
	}
	wDomainUnder := httptest.NewRecorder()
	cDomainUnder, _ := gin.CreateTestContext(wDomainUnder)
	if !quotaCheck(cDomainUnder, quotaStore, 1, "domain") {
		t.Fatal("quotaCheck should allow 5 domains under tenant override limit 10")
	}

	quotaStore.domains = 10
	wDomainMax := httptest.NewRecorder()
	cDomainMax, _ := gin.CreateTestContext(wDomainMax)
	if quotaCheck(cDomainMax, quotaStore, 1, "domain") {
		t.Fatal("quotaCheck should block when domains reach tenant override limit 10")
	}

	quotaStore.mailboxes = 20
	wMailboxMax := httptest.NewRecorder()
	cMailboxMax, _ := gin.CreateTestContext(wMailboxMax)
	if quotaCheck(cMailboxMax, quotaStore, 1, "mailbox") {
		t.Fatal("quotaCheck should block when mailboxes reach platform limit 20")
	}
}

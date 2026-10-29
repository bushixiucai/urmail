package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api"
	"github.com/bushixiucai/urmail/internal/domain/panelpassword"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestAIQuotaMissingProviderAuthenticatedRoutesPG(t *testing.T) {
	pool, app := testdb.OpenMigrated(t)
	scalar := func(query string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := pool.QueryRow(t.Context(), query, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	execute := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	brand := scalar("INSERT INTO brands(slug,name)VALUES('default','Missing provider fixture')RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status)VALUES($1,'Missing provider fixture','missing-provider','active')RETURNING id", brand)
	const password = "Owned-missing-provider-password!42"
	hash, err := panelpassword.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	actor := scalar("INSERT INTO users(email,password_hash)VALUES('missing-provider@example.test',$1)RETURNING id", hash)
	execute("INSERT INTO user_tenant_roles(user_id,tenant_id,role)VALUES($1,$2,'owner')", actor, tenant)
	execute("INSERT INTO platform_admins(user_id)VALUES($1)", actor)
	messages, err := i18n.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.New(api.Options{PlatformPool: pool, AppPool: app, Logger: zap.NewNop(), Messages: messages}))
	defer server.Close()
	raw, _ := json.Marshal(map[string]string{"email": "missing-provider@example.test", "password": password})
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/v1/auth/login", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", server.URL)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var login struct {
		Token string `json:"token"`
		CSRF  string `json:"csrf_token"`
	}
	err = json.NewDecoder(response.Body).Decode(&login)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || login.Token == "" || login.CSRF == "" {
		t.Fatalf("normal login status=%d err=%v", response.StatusCode, err)
	}
	for _, path := range []string{"/api/v1/ai/budget", "/api/v1/platform/tenants/" + strconv.FormatInt(tenant, 10) + "/ai/budget"} {
		request, err = http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+login.Token)
		request.Header.Set("X-CSRF-Token", login.CSRF)
		request.Header.Set("X-Tenant-ID", strconv.FormatInt(tenant, 10))
		request.Header.Set("Origin", server.URL)
		response, err = server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		var failure struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if readErr != nil || json.Unmarshal(body, &failure) != nil || response.StatusCode != http.StatusConflict || failure.Error.Code != "AI_DISABLED" {
			t.Fatalf("missing provider %s: status=%d body=%s err=%v", path, response.StatusCode, body, readErr)
		}
	}
	var count int
	if err = pool.QueryRow(t.Context(), "SELECT count(*) FROM ai_provider_configs WHERE tenant_id=$1", tenant).Scan(&count); err != nil || count != 0 {
		t.Fatal("read-only missing-provider GET created configuration", count, err)
	}
}

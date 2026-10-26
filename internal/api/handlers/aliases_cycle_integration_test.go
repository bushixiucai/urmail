package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

// MAIL-02: a cross-tenant alias cycle must be rejected at write time with a
// boolean answer; the response must not leak the other tenant's rules.
func TestAliasCreateRejectsCrossTenantCycle(t *testing.T) {
	platform, app := authIntegrationDatabase(t)
	ctx := context.Background()

	seed := func(name, fqdn string) int64 {
		t.Helper()
		var brand, tenant, domain int64
		if err := platform.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES($1,$1) RETURNING id", name).Scan(&brand); err != nil {
			t.Fatal(err)
		}
		if err := platform.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,$2,$3,'active') RETURNING id", brand, name, name).Scan(&tenant); err != nil {
			t.Fatal(err)
		}
		if err := platform.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,provisioning_state,verification_token,verified_at) VALUES($1,$2,'active','ready',$3,now()) RETURNING id", tenant, fqdn, strings.Repeat(name, 8)).Scan(&domain); err != nil {
			t.Fatal(err)
		}
		_ = domain
		return tenant
	}
	seed("cycleb", "b-cycle.test")
	tenantA := seed("cyclea", "a-cycle.test")

	// An owner for tenant A: RequireTenantTx resolves the session role before
	// running the handler, exactly like production.
	var ownerA int64
	if err := platform.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES($1,'fixture-only') RETURNING id", "owner@a-cycle.test").Scan(&ownerA); err != nil {
		t.Fatal(err)
	}
	if _, err := platform.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", ownerA, tenantA); err != nil {
		t.Fatal(err)
	}

	var domainB int64
	if err := platform.QueryRow(ctx, "SELECT id FROM domains WHERE fqdn='b-cycle.test'").Scan(&domainB); err != nil {
		t.Fatal(err)
	}
	// Tenant B already forwards loop@b-cycle.test back at tenant A's address.
	var tenantB int64
	if err := platform.QueryRow(ctx, "SELECT tenant_id FROM domains WHERE fqdn='b-cycle.test'").Scan(&tenantB); err != nil {
		t.Fatal(err)
	}
	if _, err := platform.Exec(ctx, "INSERT INTO mailbox_aliases(tenant_id,domain_id,local_part,target,kind) VALUES($1,$2,'loop','loop@a-cycle.test','alias')", tenantB, domainB); err != nil {
		t.Fatal(err)
	}
	var domainA int64
	if err := platform.QueryRow(ctx, "SELECT id FROM domains WHERE fqdn='a-cycle.test'").Scan(&domainA); err != nil {
		t.Fatal(err)
	}

	handler := NewAliasHandler(store.New(app.Pool), nil)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api/v1")
	// Production runs tenant handlers inside the RLS transaction; mirror that
	// so the app role sees exactly what a real request would see.
	group.Use(func(c *gin.Context) {
		c.Set(middleware.ContextTenantID, tenantA)
		c.Set(middleware.ContextUserID, ownerA)
		c.Next()
	}, middleware.RequireTenantTx(app, nil))
	group.POST("/aliases", handler.Create)

	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/aliases", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		return w
	}

	// The reverse edge would close loop@a-cycle.test -> loop@b-cycle.test.
	w := post(`{"domain_id":` + itoa(domainA) + `,"local_part":"loop","target":"loop@b-cycle.test","kind":"alias"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("cross-tenant cycle accepted: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ALIAS_CYCLE") {
		t.Fatalf("missing ALIAS_CYCLE code: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "b-cycle.test") {
		t.Fatalf("response leaked the other tenant's alias graph: %s", w.Body.String())
	}

	// A benign external forward must still succeed.
	w = post(`{"domain_id":` + itoa(domainA) + `,"local_part":"info","target":"elsewhere@outside.test","kind":"forward"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("benign alias rejected: %d %s", w.Code, w.Body.String())
	}
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}

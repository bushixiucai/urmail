package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/domain/panelpassword"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestBrandHostLoginMePlatformAndRegistrationBoundaryPG(t *testing.T) {
	platform, _ := authIntegrationDatabase(t)
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var id int64
		if e := platform.QueryRow(t.Context(), sql, args...).Scan(&id); e != nil {
			t.Fatal(e)
		}
		return id
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := platform.Exec(t.Context(), sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	a := scalar("INSERT INTO brands(slug,name)VALUES('review-a','A')RETURNING id")
	b := scalar("INSERT INTO brands(slug,name)VALUES('review-b','B')RETURNING id")
	password := "Brand-review-fixture-password!42"
	hash, err := panelpassword.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	user := scalar("INSERT INTO users(email,password_hash)VALUES('brand-review@example.test',$1)RETURNING id", hash)
	ta := scalar("INSERT INTO tenants(brand_id,name,slug,status)VALUES($1,'A','review-a','active')RETURNING id", a)
	tb := scalar("INSERT INTO tenants(brand_id,name,slug,status)VALUES($1,'B','review-b','active')RETURNING id", b)
	exec("INSERT INTO user_tenant_roles(tenant_id,user_id,role)VALUES($1,$3,'owner'),($2,$3,'owner')", ta, tb, user)
	exec("INSERT INTO platform_admins(user_id)VALUES($1)", user)
	exec("INSERT INTO brand_hosts(hostname,brand_id,status,verified_at,cert_issued_at,activated_at)VALUES('a.example.test',$1,'active',now(),now(),now()),('b.example.test',$2,'active',now(),now(),now())", a, b)
	box, err := secrets.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	auth := NewAuthHandler(store.New(platform))
	registration := RegistrationHandler{Pool: platform, Secrets: box, Enabled: true, BaseURL: "https://panel.example.test"}
	router := gin.New()
	router.Use(middleware.BrandHostBoundary(platform, "panel.example.test"))
	router.POST("/login", auth.Login)
	router.POST("/register", registration.Register)
	router.POST("/verify", registration.Verify)
	router.GET("/me", middleware.RequireAuth(store.New(platform)), auth.Me)
	router.GET("/platform", middleware.RequireAuth(store.New(platform)), middleware.RequirePlatformAdmin(store.New(platform)), func(c *gin.Context) { c.Status(204) })
	request := func(host, method, path string, body any, token string) (int, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequestWithContext(t.Context(), method, "https://"+host+path, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-Host", "a.example.test")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	status, out := request("a.example.test", http.MethodPost, "/login", map[string]string{"email": "brand-review@example.test", "password": password}, "")
	if status != 200 || len(out["tenants"].([]any)) != 1 || out["is_admin"] != false {
		t.Fatalf("brand login leaked candidates: %d %v", status, out)
	}
	token := out["token"].(string)
	if status, alias := request("a.example.test.:443", http.MethodPost, "/login", map[string]string{"email": "brand-review@example.test", "password": password}, ""); status != 200 || len(alias["tenants"].([]any)) != 1 || alias["is_admin"] != false {
		t.Fatalf("absolute DNS name and port bypassed brand lookup: %d %v", status, alias)
	}
	if status, out = request("a.example.test", http.MethodGet, "/me", nil, token); status != 200 || len(out["tenants"].([]any)) != 1 || out["is_admin"] != false {
		t.Fatalf("Me leaked cross-brand metadata: %d %v", status, out)
	}
	if status, _ = request("a.example.test", http.MethodGet, "/platform", nil, token); status != 403 {
		t.Fatal("brand entry gained global platform authority")
	}
	if status, _ = request("b.example.test", http.MethodGet, "/me", nil, token); status != 401 {
		t.Fatal("brand token replayed across hosts")
	}
	if status, _ = request("unknown.example.test", http.MethodGet, "/me", nil, token); status != 401 {
		t.Fatal("forwarded host bypassed session origin")
	}
	exec("UPDATE sessions SET origin_host='' WHERE id=$1", middleware.HashSessionToken(token))
	if status, _ = request("panel.example.test", http.MethodGet, "/me", nil, token); status != 200 {
		t.Fatal("legacy canonical migration broke")
	}
	if status, _ = request("unknown.example.test", http.MethodGet, "/me", nil, token); status != 401 {
		t.Fatal("legacy session accepted arbitrary host")
	}
	registrationBody := map[string]string{"email": "brand-registration@example.test", "password": password, "name": "Brand registration", "brand": "review-b"}
	if status, _ = request("a.example.test", http.MethodPost, "/register", registrationBody, ""); status != 403 {
		t.Fatal("body brand bypassed actual registration host")
	}
	delete(registrationBody, "brand")
	if status, out = request("a.example.test", http.MethodPost, "/register", registrationBody, ""); status != 202 {
		t.Fatalf("own brand default registration rejected %d %v", status, out)
	}
	var encrypted string
	if e := platform.QueryRow(t.Context(), "SELECT secret_payload FROM notification_outbox WHERE recipient='brand-registration@example.test'").Scan(&encrypted); e != nil {
		t.Fatal(e)
	}
	proof, err := box.DecryptString(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ = request("b.example.test", http.MethodPost, "/verify", map[string]string{"token": proof}, ""); status != 403 {
		t.Fatal("registration proof replayed to another brand")
	}
	if status, out = request("a.example.test", http.MethodPost, "/verify", map[string]string{"token": proof}, ""); status != 201 {
		t.Fatalf("wrong-brand attempt consumed original registration: %d %v", status, out)
	}
	if got := scalar("SELECT brand_id FROM tenants WHERE id=$1", int64(out["tenant_id"].(float64))); got != a {
		t.Fatal("registered tenant used another brand")
	}
}

func TestBrandHostActiveTransitionRejectsRevokedActorPG(t *testing.T) {
	platform, _ := authIntegrationDatabase(t)
	var brand, actor int64
	if e := platform.QueryRow(t.Context(), "INSERT INTO brands(slug,name)VALUES('brand-cas-review','CAS review')RETURNING id").Scan(&brand); e != nil {
		t.Fatal(e)
	}
	if e := platform.QueryRow(t.Context(), "INSERT INTO users(email,password_hash)VALUES('brand-cas-actor@example.test','fixture')RETURNING id").Scan(&actor); e != nil {
		t.Fatal(e)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := platform.Exec(t.Context(), sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec("INSERT INTO platform_admins(user_id)VALUES($1)", actor)
	exec("INSERT INTO brand_hosts(hostname,brand_id,status,verified_at,cert_issued_at)VALUES('cas.brand.example.test',$1,'cert-issued',now(),now())", brand)
	q := store.New(platform)
	row, e := q.PlatformGetBrandHostLifecycle(t.Context(), "cas.brand.example.test")
	if e != nil {
		t.Fatal(e)
	}
	transition := store.PlatformBrandHostCertificateTransitionParams{ActorID: actor, Hostname: row.Hostname, BrandID: brand, VerifiedAt: row.VerifiedAt, ExpectedStatus: "cert-issued", NextStatus: "active"}
	exec("DELETE FROM platform_admins WHERE user_id=$1", actor)
	if n, e := q.PlatformBrandHostCertificateTransition(t.Context(), transition); e != nil || n != 0 {
		t.Fatalf("revoked admin activated host: %d %v", n, e)
	}
	exec("INSERT INTO platform_admins(user_id)VALUES($1)", actor)
	exec("UPDATE users SET status='disabled' WHERE id=$1", actor)
	if n, e := q.PlatformBrandHostCertificateTransition(t.Context(), transition); e != nil || n != 0 {
		t.Fatalf("disabled admin activated host: %d %v", n, e)
	}
	current, e := q.PlatformGetBrandHostLifecycle(t.Context(), row.Hostname)
	if e != nil || current.Status != "cert-issued" || current.ActivatedAt.Valid {
		t.Fatal("denial changed issuance/activation facts", current, e)
	}
	exec("UPDATE users SET status='active' WHERE id=$1", actor)
	if n, e := q.PlatformBrandHostCertificateTransition(t.Context(), transition); e != nil || n != 1 {
		t.Fatalf("current live admin control refused: %d %v", n, e)
	}
}

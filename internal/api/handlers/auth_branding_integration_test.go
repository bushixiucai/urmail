package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pquerna/otp/totp"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
)

// Each invocation owns one newly created database. No test truncates, migrates
// down, or drops the database supplied by the developer or another test package.
func authIntegrationDatabase(t *testing.T) (*db.Pool, *db.Pool) {
	t.Helper()
	dsn := os.Getenv("URMAIL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("URMAIL_TEST_DATABASE_URL is required for real-role auth integration")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	if config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1" {
		t.Fatal("auth test fixture requires a loopback PostgreSQL host")
	}
	admin, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("urmail_auth_test_%d", time.Now().UnixNano())
	if _, err = admin.Exec(context.Background(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(context.Background())
		t.Fatal(err)
	}
	var platform, app *db.Pool
	t.Cleanup(func() {
		if platform != nil {
			platform.Close()
		}
		if app != nil {
			app.Close()
		}
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("drop own auth fixture: %v", err)
		}
		_ = admin.Close(context.Background())
	})
	target, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	target.Path = "/" + name
	// Cluster roles are shared even though these fixtures own different DBs.
	// Serialize bootstrap on the original database connection, not the new DB.
	if _, err = admin.Exec(context.Background(), "SELECT pg_advisory_lock(7750360020)"); err != nil {
		t.Fatal(err)
	}
	bootstrapErr := db.Bootstrap(context.Background(), target.String())
	_, unlockErr := admin.Exec(context.Background(), "SELECT pg_advisory_unlock(7750360020)")
	if bootstrapErr != nil {
		t.Fatal(bootstrapErr)
	}
	if unlockErr != nil {
		t.Fatal(unlockErr)
	}
	migrationURL := *target
	params := migrationURL.Query()
	params.Set("role", "urmail_migrate")
	migrationURL.RawQuery = params.Encode()
	migrator, err := db.NewMigrator(migrationURL.String())
	if err != nil {
		t.Fatal(err)
	}
	err = migrator.Up(context.Background())
	closeErr := migrator.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	platform, err = db.OpenPlatform(context.Background(), target.String())
	if err != nil {
		t.Fatal(err)
	}
	app, err = db.OpenApp(context.Background(), target.String())
	if err != nil {
		t.Fatal(err)
	}
	var role string
	if err = platform.QueryRow(context.Background(), "SELECT current_user").Scan(&role); err != nil || role != db.RolePlatform {
		t.Fatal("integration platform role is not active")
	}
	if err = app.QueryRow(context.Background(), "SELECT current_user").Scan(&role); err != nil || role != db.RoleApp {
		t.Fatal("integration app role is not active")
	}
	return platform, app
}

func TestAuthCredentialsAndBrandingWithRealRoles(t *testing.T) {
	resetMFAState()
	gin.SetMode(gin.TestMode)
	platform, app := authIntegrationDatabase(t)
	ctx := context.Background()
	q := store.New(platform.Pool)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := platform.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var value int64
		if err := platform.QueryRow(ctx, sql, args...).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	secret := "JBSWY3DPEHPK3PXP"
	password := "correct-horse-battery-staple"
	hash, err := mail.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	uid := scalar("INSERT INTO users(email,password_hash,mfa_enabled,mfa_secret) VALUES('admin@example.test',$1,true,$2) RETURNING id", hash, secret)
	exec("INSERT INTO platform_admins(user_id) VALUES($1)", uid)
	defaultBrand := scalar("INSERT INTO brands(slug,name) VALUES('default','Default') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status,owner_user_id) VALUES($1,'Tenant','tenant','active',$2) RETURNING id", defaultBrand, uid)
	exec("INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", uid, tenant)
	domain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verification_token) VALUES($1,'example.test','active','test-verification') RETURNING id", tenant)
	mailbox := scalar("INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status,send_enabled) VALUES($1,$2,'test','test@example.test',$3,1048576,'active',true) RETURNING id", tenant, domain, hash)
	auth := NewAuthHandler(q)
	legacy := NewLegacyHandler(q, q, tenant, "integration")
	branding := NewBrandingHandler(q)
	r := gin.New()
	r.POST("/login", auth.Login)
	r.POST("/verify", auth.MFAVerify)
	r.GET("/branding", branding.Public)
	user := r.Group("/auth")
	user.Use(middleware.RequireAuth(q), middleware.RequireCSRF())
	user.POST("/legacy-session", auth.CreateLegacySession)
	user.GET("/credentials", auth.ListApplicationCredentials)
	user.POST("/credentials", auth.CreateApplicationCredential)
	user.DELETE("/credentials/:id", auth.RevokeApplicationCredential)
	managed := r.Group("/platform")
	managed.Use(middleware.RequireAuth(q), middleware.RequirePlatformAdmin(q), middleware.RequireCSRF())
	managed.POST("/brands", branding.Save)
	managed.DELETE("/brands/:id", branding.Delete)
	managed.PATCH("/brands/:id/status", branding.SetStatus)
	managed.POST("/hosts", branding.BindHost)
	r.GET("/admin/system/status", legacy.Status)
	r.POST("/admin/mail/users/password", legacy.SetMailUserPassword)
	request := func(method, path string, body any, token, csrf, basic string) (int, map[string]any, string) {
		t.Helper()
		var encoded string
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			encoded = string(raw)
		}
		req := httptest.NewRequestWithContext(context.Background(), method, "https://panel.example.test"+path, strings.NewReader(encoded))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		if basic != "" {
			req.SetBasicAuth("admin@example.test", basic)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		out := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out, w.Body.String()
	}
	code := func() string {
		v, err := totp.GenerateCode(secret, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	status, challenge, _ := request("POST", "/login", map[string]any{"email": "admin@example.test", "password": password}, "", "", "")
	if status != 200 || challenge["mfa_required"] != true {
		t.Fatal("first leg must require MFA")
	}
	status, session, _ := request("POST", "/verify", map[string]any{"mfa_token": challenge["mfa_token"], "code": code()}, "", "", "")
	if status != 200 {
		t.Fatalf("second leg status=%d", status)
	}
	token := session["token"].(string)
	csrf := session["csrf_token"].(string)
	if status, _, _ := request("GET", "/admin/system/status", nil, "", "", password); status != 403 {
		t.Fatalf("MFA password bypass status=%d", status)
	}
	if status, _, _ := request("GET", "/admin/system/status", nil, token, "", ""); status != 200 {
		t.Fatalf("MFA bearer legacy status=%d", status)
	}
	if status, _, _ := request("POST", "/auth/credentials", map[string]any{"name": "missing factor", "scopes": []string{"legacy:read"}, "expires_at": time.Now().Add(time.Hour)}, token, csrf, ""); status != 400 {
		t.Fatal("fresh MFA must be supplied")
	}
	if status, _, _ := request("POST", "/auth/credentials", map[string]any{"name": "wrong factor", "scopes": []string{"legacy:read"}, "expires_at": time.Now().Add(time.Hour), "code": "invalid"}, token, csrf, ""); status != 401 {
		t.Fatal("wrong fresh MFA must be rejected")
	}
	create := func(scopes []string) (string, int64) {
		t.Helper()
		status, out, body := request("POST", "/auth/credentials", map[string]any{"name": "backup job", "scopes": scopes, "expires_at": time.Now().Add(time.Hour), "code": code()}, token, csrf, "")
		if status != 201 {
			t.Fatalf("credential create status=%d body=%s", status, body)
		}
		credential := out["credential"].(map[string]any)
		return out["token"].(string), int64(credential["id"].(float64))
	}
	readCredential, readID := create([]string{"legacy:read"})
	otherUser := scalar("INSERT INTO users(email,password_hash) VALUES('other@example.test',$1) RETURNING id", hash)
	_, err = q.CreateSession(ctx, store.CreateSessionParams{ID: middleware.HashSessionToken("other-panel"), UserID: otherUser, CsrfToken: "other-csrf", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	if status, _, body := request("GET", "/auth/credentials", nil, "other-panel", "", ""); status != 200 || strings.Contains(body, "backup job") {
		t.Fatal("credential list crossed owner boundary")
	}
	if status, _, _ := request("DELETE", "/auth/credentials/"+strconv.FormatInt(readID, 10), nil, "other-panel", "other-csrf", ""); status != 404 {
		t.Fatal("credential revocation crossed owner boundary")
	}
	if status, _, _ := request("POST", "/auth/credentials", map[string]any{"name": "unauthorized", "scopes": []string{"legacy:read"}, "expires_at": time.Now().Add(time.Hour), "code": code()}, "other-panel", "other-csrf", ""); status != 403 {
		t.Fatal("non-administrator minted legacy credentials")
	}
	exec("INSERT INTO platform_admins(user_id) VALUES($1)", otherUser)
	if status, _, _ := request("POST", "/auth/credentials", map[string]any{"name": "no mfa", "scopes": []string{"legacy:read"}, "expires_at": time.Now().Add(time.Hour), "code": code()}, "other-panel", "other-csrf", ""); status != 403 {
		t.Fatal("administrator without MFA minted application credentials")
	}

	var persisted string
	if err = platform.QueryRow(ctx, "SELECT token_hash FROM application_credentials WHERE id=$1", readID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != middleware.HashSessionToken(readCredential) || persisted == readCredential {
		t.Fatal("credential secret was not hash-only")
	}
	if status, _, body := request("GET", "/auth/credentials", nil, token, "", ""); status != 200 || strings.Contains(body, readCredential) || strings.Contains(body, "token_hash") {
		t.Fatal("credential list exposed a token")
	}
	if _, err := app.Exec(ctx, "SELECT token_hash FROM application_credentials"); err == nil {
		t.Fatal("app role may not access credential verifiers")
	}
	if _, err := app.Exec(ctx, "SELECT * FROM brand_hosts"); err == nil {
		t.Fatal("app role may not enumerate brand bindings")
	}
	if status, _, _ := request("GET", "/admin/system/status", nil, "", "", readCredential); status != 200 {
		t.Fatal("read credential rejected")
	}
	before := scalar("SELECT count(*) FROM application_credentials")
	form := httptest.NewRequestWithContext(context.Background(), "POST", "https://panel.example.test/admin/mail/users/password", strings.NewReader("email=test%40example.test&password=replacement-secret"))
	form.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	form.SetBasicAuth("admin@example.test", readCredential)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, form)
	if w.Code != 403 {
		t.Fatal("read credential gained write scope")
	}
	var unchanged string
	if err = platform.QueryRow(ctx, "SELECT password_hash FROM mailboxes WHERE id=$1", mailbox).Scan(&unchanged); err != nil || unchanged != hash {
		t.Fatal("rejected scope changed mailbox")
	}
	if before != scalar("SELECT count(*) FROM application_credentials") {
		t.Fatal("rejected request changed credentials")
	}
	writable, writeID := create([]string{"legacy:read", "legacy:write"})
	write := func() int {
		form := httptest.NewRequestWithContext(context.Background(), "POST", "https://panel.example.test/admin/mail/users/password", strings.NewReader("email=test%40example.test&password=replacement-secret"))
		form.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		form.SetBasicAuth("admin@example.test", writable)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, form)
		return w.Code
	}
	if write() != 200 {
		t.Fatal("write credential rejected")
	}
	if err = platform.QueryRow(ctx, "SELECT password_hash FROM mailboxes WHERE id=$1", mailbox).Scan(&unchanged); err != nil || !mail.VerifyPassword("replacement-secret", unchanged) {
		t.Fatal("authorized legacy mutation was not persisted")
	}
	// Creator status and role are checked live, not copied into the credential.
	exec("UPDATE users SET status='disabled' WHERE id=$1", uid)
	if status, _, _ := request("GET", "/admin/system/status", nil, "", "", writable); status != 403 {
		t.Fatal("disabled creator retained credential access")
	}
	exec("UPDATE users SET status='active' WHERE id=$1", uid)
	exec("DELETE FROM platform_admins WHERE user_id=$1", uid)
	if status, _, _ := request("GET", "/admin/system/status", nil, "", "", writable); status != 403 {
		t.Fatal("demoted creator retained credential access")
	}
	exec("INSERT INTO platform_admins(user_id) VALUES($1)", uid)
	if status, _, _ := request("DELETE", "/auth/credentials/"+strconv.FormatInt(writeID, 10), nil, token, csrf, ""); status != 200 {
		t.Fatal("credential revoke failed")
	}
	if write() != 403 {
		t.Fatal("revoked credential accepted")
	}
	exec("UPDATE application_credentials SET created_at=now()-interval '2 hours',expires_at=now()-interval '1 hour' WHERE id=$1", readID)
	if status, _, _ := request("GET", "/admin/system/status", nil, "", "", readCredential); status != 403 {
		t.Fatal("expired credential accepted")
	}
	if scalar("SELECT count(*) FROM audit_logs WHERE action='application_credential.create'") != 2 || scalar("SELECT count(*) FROM audit_logs WHERE action='application_credential.revoke'") != 1 {
		t.Fatal("credential lifecycle audit is incomplete")
	}
	if scalar("SELECT count(*) FROM audit_logs WHERE detail::text LIKE '%' || $1 || '%'", writable) != 0 {
		t.Fatal("audit exposed credential")
	}
	shortStatus, shortSession, _ := request("POST", "/auth/legacy-session", map[string]any{"code": code()}, token, csrf, "")
	if shortStatus != 201 || shortSession["csrf_token"] == nil {
		t.Fatal("fresh MFA short session failed")
	}
	shortToken := shortSession["token"].(string)
	if status, _, _ := request("GET", "/admin/system/status", nil, shortToken, "", ""); status != 200 {
		t.Fatal("short MFA session could not access legacy API")
	}
	if status, _, _ := request("GET", "/auth/credentials", nil, shortToken, "", ""); status != 401 {
		t.Fatal("legacy-only session gained general v1 access")
	}
	exec("UPDATE sessions SET expires_at=now()-interval '1 minute' WHERE id=$1", middleware.HashSessionToken(shortToken))
	if status, _, _ := request("GET", "/admin/system/status", nil, shortToken, "", ""); status != 401 {
		t.Fatal("expired short MFA session retained legacy access")
	}
	exec("UPDATE sessions SET mfa_verified_at=now()-interval '16 minutes' WHERE id=$1", middleware.HashSessionToken(token))
	if status, _, _ := request("GET", "/admin/system/status", nil, token, "", ""); status != 401 {
		t.Fatal("old MFA authorization remained valid")
	}

	t.Run("public brand selection never grants authority", func(t *testing.T) {
		hosted := scalar("INSERT INTO brands(slug,name,theme) VALUES('hosted','Hosted','{\"primary\":\"#123456\",\"database_url\":\"secret-value\"}') RETURNING id")
		linked := scalar("INSERT INTO brands(slug,name) VALUES('linked','Linked') RETURNING id")
		exec("INSERT INTO brand_hosts(hostname,brand_id) VALUES('panel.example.test',$1)", hosted)
		public := func(host, path string) (string, string) {
			req := httptest.NewRequestWithContext(context.Background(), "GET", "https://"+host+path, nil)
			req.Header.Set("X-Forwarded-Host", "panel.example.test")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatalf("public branding %d %s", w.Code, w.Body.String())
			}
			var out struct {
				Brand  PublicBrand `json:"brand"`
				Source string      `json:"source"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(w.Body.String(), "secret-value") || strings.Contains(w.Body.String(), "database_url") {
				t.Fatal("public branding leaked raw theme")
			}
			return out.Brand.Slug, out.Source
		}
		if slug, source := public("panel.example.test:443", "/branding?brand=linked"); slug != "hosted" || source != "host" {
			t.Fatal("authorized host did not win")
		}
		if slug, source := public("unbound.example.test", "/branding?brand=linked"); slug != "linked" || source != "link" {
			t.Fatal("brand link fallback failed")
		}
		if slug, _ := public("unbound.example.test", "/branding?brand=missing"); slug != "default" {
			t.Fatal("unknown brand did not fall back")
		}
		exec("UPDATE brands SET status='disabled' WHERE id=$1", linked)
		if slug, _ := public("unbound.example.test", "/branding?brand=linked"); slug != "default" {
			t.Fatal("disabled link remained public")
		}
		if status, _, _ := request("POST", "/platform/hosts", map[string]any{"hostname": "new.example.test", "brand_id": hosted}, "", "", ""); status != 401 {
			t.Fatal("public brand selection bypassed admin authentication")
		}
		// Referenced brands cannot be destroyed to satisfy a cosmetic CRUD check.
		if status, _, _ := request("DELETE", "/platform/brands/"+strconv.FormatInt(defaultBrand, 10), nil, token, csrf, ""); status != 404 && status != 409 {
			t.Fatal("default brand deleted")
		}
		exec("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Referenced','ref','active')", hosted)
		if status, _, _ := request("DELETE", "/platform/brands/"+strconv.FormatInt(hosted, 10), nil, token, csrf, ""); status != 409 {
			t.Fatal("referenced brand deletion did not conflict")
		}
	})
	// Ordinary password/session revocation is independent of explicitly revoked
	// application credentials; their owner status, scope and expiry always apply.
}

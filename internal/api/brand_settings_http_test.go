package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestBrandSettingsHTTPInheritanceAndIsolation(t *testing.T) {
	app, platform := remediationHTTPDatabase(t)
	ctx := context.Background()
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := platform.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := platform.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	a := scalar("INSERT INTO brands(slug,name) VALUES('brand-settings-a','A') RETURNING id")
	b := scalar("INSERT INTO brands(slug,name) VALUES('brand-settings-b','B') RETURNING id")
	admin := scalar("INSERT INTO users(email,password_hash) VALUES('settings-admin@v3.test','x') RETURNING id")
	low := scalar("INSERT INTO users(email,password_hash) VALUES('settings-owner@v3.test','x') RETURNING id")
	ta := scalar("INSERT INTO tenants(brand_id,name,slug) VALUES($1,'A','settings-a') RETURNING id", a)
	tb := scalar("INSERT INTO tenants(brand_id,name,slug) VALUES($1,'B','settings-b') RETURNING id", b)
	exec("INSERT INTO platform_admins(user_id) VALUES($1)", admin)
	exec("INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner'),($3,$4,'owner')", admin, ta, low, tb)
	q := store.New(platform.Pool)
	for _, v := range []struct {
		token    string
		uid, tid int64
	}{{"settings-admin", admin, ta}, {"settings-low", low, tb}} {
		if _, err := q.CreateSession(ctx, store.CreateSessionParams{ID: middleware.HashSessionToken(v.token), UserID: v.uid, TenantID: pgtype.Int8{Int64: v.tid, Valid: true}, CsrfToken: "csrf", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := i18n.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.New(api.Options{Logger: zap.NewNop(), AppPool: app, PlatformPool: platform, Messages: messages, Audit: q}))
	defer server.Close()
	call := func(method, path, token string, body any, expected int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(ctx, method, server.URL+"/api/v1"+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-CSRF-Token", "csrf")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		out := map[string]any{}
		if err = json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != expected {
			t.Fatalf("%s %s got %d want %d: %#v", method, path, res.StatusCode, expected, out)
		}
		return out
	}
	patch := func(path string, settings map[string]any) {
		t.Helper()
		call(http.MethodPatch, path, "settings-admin", map[string]any{"settings": settings}, 200)
	}
	brandA := fmt.Sprintf("/platform/brands/%d/settings", a)
	brandB := fmt.Sprintf("/platform/brands/%d/settings", b)
	patch("/platform/settings", map[string]any{"locale": "en", "mailbox_default_quota_bytes": int64(1024)})
	patch(brandA, map[string]any{"locale": "zh-CN", "mailbox_default_quota_bytes": int64(2048)})
	own := call(http.MethodGet, brandA, "settings-admin", nil, 200)["settings"].(map[string]any)
	if own["locale"] != "zh-CN" {
		t.Fatal(own)
	}
	if len(call(http.MethodGet, brandB, "settings-admin", nil, 200)["settings"].(map[string]any)) != 0 {
		t.Fatal("brand A overwrote brand B")
	}
	current := func(token string) map[string]any {
		t.Helper()
		return call(http.MethodGet, "/tenants/current", token, nil, 200)["settings"].(map[string]any)
	}
	if current("settings-admin")["locale"] != "zh-CN" || current("settings-low")["locale"] != "en" {
		t.Fatal("brand inheritance crossed tenant brand")
	}
	if current("settings-admin")["mailbox_default_quota_bytes"] != float64(2048) || current("settings-low")["mailbox_default_quota_bytes"] != float64(1024) {
		t.Fatal("brand quota inheritance crossed brands")
	}
	patch("/tenants/current", map[string]any{"locale": "en"})
	if current("settings-admin")["locale"] != "en" {
		t.Fatal("tenant did not override brand")
	}
	patch("/tenants/current", map[string]any{"locale": nil})
	if current("settings-admin")["locale"] != "zh-CN" {
		t.Fatal("tenant reset did not inherit brand")
	}
	patch(brandA, map[string]any{"locale": nil})
	if current("settings-admin")["locale"] != "en" {
		t.Fatal("brand reset did not inherit platform")
	}
	call(http.MethodPatch, brandA, "settings-admin", map[string]any{"settings": map[string]any{"locale": "zh-CN", "unapproved": nil}}, 400)
	if current("settings-admin")["locale"] != "en" {
		t.Fatal("invalid multi-key request partially wrote")
	}
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		for _, path := range []string{brandA, brandB} {
			call(method, path, "settings-low", map[string]any{"settings": map[string]any{"locale": "zh-CN"}}, 403)
		}
	}
	call(http.MethodGet, "/platform/brands/9223372036854775807/settings", "settings-admin", nil, 404)
	call(http.MethodPatch, "/platform/brands/9223372036854775807/settings", "settings-admin", map[string]any{"settings": map[string]any{"locale": "en"}}, 404)
	if n := scalar("SELECT count(*) FROM settings WHERE scope='brand' AND scope_id=$1", b); n != 0 {
		t.Fatal("cross-brand settings mutation")
	}
	// Public branding resolves only platform/brand theme; authenticated views
	// additionally resolve tenant overrides through the same domain helper.
	patch("/platform/settings", map[string]any{"theme": map[string]any{"primary": "#112233"}})
	patch(brandA, map[string]any{"theme": map[string]any{"primary": "#445566"}})
	publicTheme := func() map[string]any {
		t.Helper()
		out := call(http.MethodGet, "/branding?brand=brand-settings-a", "", nil, 200)
		brand := out["brand"].(map[string]any)
		if len(brand) != 6 {
			t.Fatalf("unexpected public DTO: %#v", brand)
		}
		if _, ok := brand["settings"]; ok {
			t.Fatal("settings leaked publicly")
		}
		return brand
	}
	tenantTheme := func() map[string]any {
		t.Helper()
		return call(http.MethodGet, "/tenants/current", "settings-admin", nil, 200)["theme"].(map[string]any)
	}
	assertPrimary := func(theme map[string]any, want string) {
		t.Helper()
		if theme["primary_color"] != want || theme["theme"].(map[string]any)["primary"] != want {
			t.Fatalf("resolved theme mismatch: %#v want %s", theme, want)
		}
	}
	assertPrimary(publicTheme(), "#445566")
	assertPrimary(tenantTheme(), "#445566")
	patch("/tenants/current", map[string]any{"theme": map[string]any{"primary": "#778899"}})
	assertPrimary(tenantTheme(), "#778899")
	assertPrimary(publicTheme(), "#445566")
	patch(brandA, map[string]any{"theme": nil})
	assertPrimary(publicTheme(), "#112233")
	assertPrimary(tenantTheme(), "#778899")
	patch("/tenants/current", map[string]any{"theme": nil})
	assertPrimary(tenantTheme(), "#112233")

}

package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/timeutil"
)

// authProxySession inserts a live session for userID and returns its raw
// token (the fake store indexes sessions by the raw id, mirroring how the
// login flow hands the raw token to the browser while the middleware hashes
// it; CheckAuthProxy hashes before lookup, so these tests index by hash).
func authProxySession(t *testing.T, f *fakeAuthStore, userID int64) (string, store.Sessions) {
	t.Helper()
	token := fmt.Sprintf("%d-%s", userID, strings.Repeat("t", 40))
	session := store.Sessions{
		ID:        middleware.HashSessionToken(token),
		UserID:    userID,
		ExpiresAt: timeutil.NowMicro() + int64(time.Hour),
	}
	f.sessions[session.ID] = session
	return token, session
}

func authProxyRouter(h *AuthHandler) *gin.Engine {
	router := gin.New()
	router.GET("/api/v1/auth/check", h.CheckAuthProxy)
	return router
}

// TestCheckAuthProxyPlatformAdminOnly is the GRAFANA-DESIGN §4 fail-closed
// contract: only a live platform-admin session earns the mailbox and role
// headers; every other credential state is a bare 403.
func TestCheckAuthProxyPlatformAdminOnly(t *testing.T) {
	f := newFakeAuthStore()
	adminID := f.addUser(t, "root@example.test", false, "", nil)
	plainID := f.addUser(t, "user@example.test", false, "", nil)
	f.admins = map[int64]bool{adminID: true}

	adminToken, _ := authProxySession(t, f, adminID)
	plainToken, _ := authProxySession(t, f, plainID)
	expiredToken := "expired-" + strings.Repeat("t", 40)
	f.sessions[middleware.HashSessionToken(expiredToken)] = store.Sessions{
		ID:        middleware.HashSessionToken(expiredToken),
		UserID:    adminID,
		ExpiresAt: timeutil.NowMicro() - 1,
	}

	h := NewAuthHandler(f)
	h.PanelDomain = "example.test"
	router := authProxyRouter(h)

	for _, tc := range []struct {
		name   string
		cookie string
		bearer string
		status int
	}{
		{"admin cookie", adminToken, "", http.StatusOK},
		{"admin bearer passthrough", "", "Bearer " + adminToken, http.StatusOK},
		{"non-admin", plainToken, "", http.StatusForbidden},
		{"expired session", expiredToken, "", http.StatusForbidden},
		{"no credential", "", "", http.StatusForbidden},
		{"garbage token", "not-a-session", "", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/auth/check", nil)
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: authProxyCookie, Value: tc.cookie})
			}
			if tc.bearer != "" {
				req.Header.Set("Authorization", tc.bearer)
			}
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)
			if resp.Code != tc.status {
				t.Fatalf("status=%d want %d", resp.Code, tc.status)
			}
			if tc.status != http.StatusOK {
				return
			}
			if got := resp.Header().Get("X-URMAIL-Mailbox"); got != "root@example.test" {
				t.Fatalf("mailbox header %q", got)
			}
			if got := resp.Header().Get("X-WEBAUTH-ROLE"); got != "Admin" {
				t.Fatalf("role header %q", got)
			}
			if got := resp.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
				t.Fatalf("cache-control %q", got)
			}
		})
	}
}

// TestLoginIssuesParentDomainAuthProxyCookie pins the cookie issuance side
// of GRAFANA-DESIGN §4: with PanelDomain set the login response carries the
// parent-domain HttpOnly session cookie; without it the historical
// localStorage-only behavior is unchanged.
func TestLoginIssuesParentDomainAuthProxyCookie(t *testing.T) {
	f := newFakeAuthStore()
	f.addUser(t, "root@example.test", false, "", nil)

	build := func(panelDomain string) (*gin.Engine, *AuthHandler) {
		h := NewAuthHandler(f)
		h.PanelDomain = panelDomain
		router := gin.New()
		router.POST("/api/v1/auth/login", h.Login)
		return router, h
	}

	for _, tc := range []struct {
		name       string
		panel      string
		proto      string
		wantCookie bool
		wantSecure bool
	}{
		{"with domain over https", "example.test", "https", true, true},
		{"with domain over http", "example.test", "http", true, false},
		{"without domain", "", "https", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router, _ := build(tc.panel)
			req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/auth/login",
				strings.NewReader(`{"email":"root@example.test","password":"correct-horse"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "https://panel.example.test")
			req.Header.Set("X-Forwarded-Proto", tc.proto)
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)
			if resp.Code != http.StatusOK {
				t.Fatalf("login status=%d body=%s", resp.Code, resp.Body.String())
			}
			cookies := resp.Result().Cookies()
			var found *http.Cookie
			for _, c := range cookies {
				if c.Name == authProxyCookie {
					found = c
				}
			}
			if tc.wantCookie {
				if found == nil {
					t.Fatalf("auth proxy cookie missing; got %v", cookies)
				}
				// RFC6265: a leading dot is optional; Domain=example.test
				// already covers every subdomain (the parent-domain cookie).
				if got := strings.TrimPrefix(found.Domain, "."); got != "example.test" {
					t.Fatalf("cookie domain %q", found.Domain)
				}
				if !found.HttpOnly {
					t.Fatal("cookie must be HttpOnly")
				}
				if found.Secure != tc.wantSecure {
					t.Fatalf("cookie secure=%v want %v", found.Secure, tc.wantSecure)
				}
				if found.MaxAge <= 0 {
					t.Fatalf("cookie max age %d", found.MaxAge)
				}
			} else if found != nil {
				t.Fatalf("unexpected auth proxy cookie %v", found)
			}
		})
	}
	_ = pgtype.Text{}
}

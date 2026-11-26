package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/timeutil"
)

// scopedAuthStore wraps fakeAuthStore with the production scope contract:
// the platform-scoped store methods mirror what isolation.WrapQuerier
// enforces at runtime (QA PR150 P0-1 — the unscoped fake masked a live 403
// because CheckAuthProxy passed a bare request context). It also replays
// the GetSessionByID SQL joins: the row only resolves while the owning user
// is active.
type scopedAuthStore struct {
	*fakeAuthStore
}

func (s *scopedAuthStore) check(ctx context.Context, method string) error {
	_, err := isolation.CheckMethod(ctx, method, isolation.ScopePlatform)
	return err
}

func (s *scopedAuthStore) GetSessionByID(ctx context.Context, id string) (store.Sessions, error) {
	if err := s.check(ctx, "GetSessionByID"); err != nil {
		return store.Sessions{}, err
	}
	session, err := s.fakeAuthStore.GetSessionByID(ctx, id)
	if err != nil {
		return session, err
	}
	// Mirror the SQL join (auth.sql): sessions resolve only for active users.
	if user, ok := s.users[session.UserID]; !ok || user.Status != "active" {
		return store.Sessions{}, context.Canceled
	}
	return session, nil
}

func (s *scopedAuthStore) CheckPlatformAdmin(ctx context.Context, userID int64) (bool, error) {
	if err := s.check(ctx, "CheckPlatformAdmin"); err != nil {
		return false, err
	}
	return s.fakeAuthStore.CheckPlatformAdmin(ctx, userID)
}

func (s *scopedAuthStore) GetUserByID(ctx context.Context, id int64) (store.GetUserByIDRow, error) {
	if err := s.check(ctx, "GetUserByID"); err != nil {
		return store.GetUserByIDRow{}, err
	}
	return s.fakeAuthStore.GetUserByID(ctx, id)
}

// TestCheckAuthProxyRequiresPlatformScope proves the fix for QA PR150 P0-1:
// the production ScopedQuerier rejects a bare request context, so the
// handler must derive authScopeContext before every platform store call.
// Under the scope-checking stub a missing scope fails the whole chain (403)
// while a properly scoped super-admin session earns the 200 + headers.
func TestCheckAuthProxyRequiresPlatformScope(t *testing.T) {
	f := &fakeAuthStore{
		users:       map[int64]*store.GetUserByIDRow{},
		byEmail:     map[string]int64{},
		sessions:    map[string]store.Sessions{},
		usedCodes:   map[int64]map[string]bool{},
		admins:      map[int64]bool{},
		currentUser: 0,
	}
	adminID := f.addUser(t, "root@example.test", false, "", nil)
	f.admins[adminID] = true

	// A disabled super-admin must not pass the GetSessionByID join either.
	disabledID := f.addUser(t, "disabled@example.test", false, "", nil)
	f.admins[disabledID] = true
	f.users[disabledID].Status = "suspended"

	token := strings.Repeat("s", 40)
	live := store.Sessions{
		ID:        middleware.HashSessionToken(token),
		UserID:    adminID,
		ExpiresAt: timeutil.NowMicro() + int64(time.Hour),
	}
	f.sessions[live.ID] = live
	disabledToken := strings.Repeat("d", 40)
	f.sessions[middleware.HashSessionToken(disabledToken)] = store.Sessions{
		ID:        middleware.HashSessionToken(disabledToken),
		UserID:    disabledID,
		ExpiresAt: timeutil.NowMicro() + int64(time.Hour),
	}

	h := NewAuthHandler(&scopedAuthStore{fakeAuthStore: f})
	h.PanelDomain = "example.test"
	router := gin.New()
	router.GET("/api/v1/auth/check", h.CheckAuthProxy)

	for _, tc := range []struct {
		name   string
		token  string
		status int
	}{
		{"live platform admin", token, http.StatusOK},
		{"disabled super admin", disabledToken, http.StatusForbidden},
		{"unknown session", "gone", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/auth/check", nil)
			req.AddCookie(&http.Cookie{Name: authProxyCookie, Value: tc.token})
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)
			if resp.Code != tc.status {
				t.Fatalf("status=%d want %d", resp.Code, tc.status)
			}
			if tc.status == http.StatusOK {
				if got := resp.Header().Get("X-WEBAUTH-ROLE"); got != "Admin" {
					t.Fatalf("role header %q", got)
				}
				if got := resp.Header().Get("X-URMAIL-Mailbox"); got != "root@example.test" {
					t.Fatalf("mailbox header %q", got)
				}
			}
		})
	}
}

// TestLogoutExpiresAuthProxyCookie pins the P1-3 logout behavior: with a
// panel domain configured, logout expires the parent-domain cookie so the
// browser stops replaying the dead credential.
func TestLogoutExpiresAuthProxyCookie(t *testing.T) {
	f := newFakeAuthStore()
	h := NewAuthHandler(f)
	h.PanelDomain = "example.test"
	router := gin.New()
	router.POST("/api/v1/auth/logout", h.Logout)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/auth/logout", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("logout status=%d", resp.Code)
	}
	var expired *http.Cookie
	for _, c := range resp.Result().Cookies() {
		if c.Name == authProxyCookie {
			expired = c
		}
	}
	if expired == nil || expired.MaxAge != -1 {
		t.Fatalf("logout did not expire the auth proxy cookie: %+v", expired)
	}
}

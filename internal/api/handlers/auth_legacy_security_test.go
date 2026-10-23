package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp/totp"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

type concurrentAuthStore struct {
	*fakeAuthStore
	gate    chan struct{}
	arrived chan struct{}
	mu      sync.Mutex
}

func (s *concurrentAuthStore) GetUserByID(c context.Context, id int64) (store.GetUserByIDRow, error) {
	s.arrived <- struct{}{}
	<-s.gate
	return s.fakeAuthStore.GetUserByID(c, id)
}
func (s *concurrentAuthStore) CreateSession(c context.Context, p store.CreateSessionParams) (store.Sessions, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fakeAuthStore.CreateSession(c, p)
}

func TestMFAVerificationConsumesAuthorizationBeforeCreatingSession(t *testing.T) {
	resetMFAState()
	gin.SetMode(gin.TestMode)
	st := newFakeAuthStore()
	secret := "JBSWY3DPEHPK3PXP"
	id := st.addUser(t, "concurrent@example.test", true, secret, nil)
	parallel := &concurrentAuthStore{fakeAuthStore: st, gate: make(chan struct{}), arrived: make(chan struct{}, mfaMaxVerifyAttempts)}
	token, _ := pendingMFATokens.issue(id, "127.0.0.1|concurrent@example.test")
	code, _ := totp.GenerateCode(secret, time.Now())
	payload, _ := json.Marshal(map[string]string{"mfa_token": token, "code": code})
	r := gin.New()
	r.POST("/verify", NewAuthHandler(parallel).MFAVerify)
	statuses := make(chan int, 20)
	for i := 0; i < 20; i++ {
		go func() {
			w := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(context.Background(), "POST", "/verify", strings.NewReader(string(payload)))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			statuses <- w.Code
		}()
	}
	for i := 0; i < mfaMaxVerifyAttempts; i++ {
		<-parallel.arrived
	}
	close(parallel.gate)
	successes := 0
	for i := 0; i < 20; i++ {
		if <-statuses == http.StatusOK {
			successes++
		}
	}
	if successes != 1 || len(st.sessions) != 1 {
		t.Fatalf("expected one session from 20 requests, successes=%d sessions=%d", successes, len(st.sessions))
	}
}

type legacyAuthFixture struct {
	*fakeAuthStore
	admin         bool
	legacySession store.GetLegacySessionRow
	credential    store.AuthenticateApplicationCredentialRow
	audits        int
}

func (s *legacyAuthFixture) CheckPlatformAdmin(context.Context, int64) (bool, error) {
	return s.admin, nil
}
func (s *legacyAuthFixture) GetLegacySession(_ context.Context, hash string) (store.GetLegacySessionRow, error) {
	if s.legacySession.ID == hash {
		return s.legacySession, nil
	}
	return store.GetLegacySessionRow{}, pgx.ErrNoRows
}
func (s *legacyAuthFixture) AuthenticateApplicationCredential(_ context.Context, p store.AuthenticateApplicationCredentialParams) (store.AuthenticateApplicationCredentialRow, error) {
	if p.TokenHash == hashApplicationTestToken() && p.Email == "admin@example.test" && s.admin {
		return s.credential, nil
	}
	return store.AuthenticateApplicationCredentialRow{}, pgx.ErrNoRows
}
func (s *legacyAuthFixture) AuditLegacyCredentialUse(context.Context, store.AuditLegacyCredentialUseParams) error {
	s.audits++
	return nil
}
func hashApplicationTestToken() string {
	return middleware.HashSessionToken(applicationTokenPrefix + "test")
}

func TestLegacyBasicHonorsMFAAndRateLimits(t *testing.T) {
	resetMFAState()
	st := &legacyAuthFixture{fakeAuthStore: newFakeAuthStore(), admin: true}
	st.addUser(t, "admin@example.test", true, "JBSWY3DPEHPK3PXP", nil)
	h := NewLegacyHandler(st, nil, 1, "test")
	r := gin.New()
	r.GET("/status", h.Status)
	for i := 0; i <= loginMaxFailures; i++ {
		req := httptest.NewRequestWithContext(context.Background(), "GET", "/status", nil)
		req.SetBasicAuth("admin@example.test", "correct-horse")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		want := http.StatusForbidden
		if i == loginMaxFailures {
			want = http.StatusTooManyRequests
		}
		if w.Code != want {
			t.Fatalf("attempt %d status=%d want=%d", i, w.Code, want)
		}
	}
	if st.audits != 0 {
		t.Fatal("rejected password was audited as authorized")
	}
}
func TestLegacyApplicationScopesAndSessionCSRF(t *testing.T) {
	resetMFAState()
	st := &legacyAuthFixture{fakeAuthStore: newFakeAuthStore(), admin: true, credential: store.AuthenticateApplicationCredentialRow{ID: 9, UserID: 1, Scopes: []string{"legacy:read"}}}
	st.legacySession = store.GetLegacySessionRow{ID: middleware.HashSessionToken("valid-session"), UserID: 1, CsrfToken: "csrf"}
	h := NewLegacyHandler(st, nil, 1, "test")
	r := gin.New()
	r.GET("/status", h.Status)
	r.POST("/write", func(c *gin.Context) {
		if h.authorize(c) {
			c.Status(http.StatusNoContent)
		}
	})
	cases := []struct {
		name, method, auth, origin, csrf string
		want                             int
	}{
		{"read credential", "GET", "basic", "", "", 200},
		{"read scope cannot write", "POST", "basic", "", "", 403},
		{"cross site write rejected", "POST", "basic", "https://evil.example", "", 403},
		{"session write requires csrf", "POST", "bearer", "", "", 403},
		{"session write valid csrf", "POST", "bearer", "", "csrf", 204},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := "/status"
			if tc.method == "POST" {
				path = "/write"
			}
			req := httptest.NewRequestWithContext(context.Background(), tc.method, path, nil)
			if tc.auth == "basic" {
				req.SetBasicAuth("admin@example.test", applicationTokenPrefix+"test")
			} else {
				req.Header.Set("Authorization", "Bearer valid-session")
			}
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.csrf != "" {
				req.Header.Set("X-CSRF-Token", tc.csrf)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

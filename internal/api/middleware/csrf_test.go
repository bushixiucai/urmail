package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestCSRFMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		method     string
		origin     string
		referer    string
		csrfHeader string
		session    *store.Sessions
		wantStatus int
	}{
		{
			name:       "safe GET passes without check",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
		},
		{
			name:       "POST without session passes to next handler",
			method:     http.MethodPost,
			wantStatus: http.StatusOK,
		},
		{
			name:       "POST with empty session csrf token returns 403",
			method:     http.MethodPost,
			session:    &store.Sessions{CsrfToken: ""},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "POST with invalid Origin returns 403",
			method:     http.MethodPost,
			origin:     "http://evil.com",
			csrfHeader: "valid-token",
			session:    &store.Sessions{CsrfToken: "valid-token"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "POST with invalid Referer returns 403",
			method:     http.MethodPost,
			referer:    "http://evil.com/phishing",
			csrfHeader: "valid-token",
			session:    &store.Sessions{CsrfToken: "valid-token"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "POST with invalid CSRF token returns 403",
			method:     http.MethodPost,
			origin:     "http://example.com",
			csrfHeader: "wrong-token",
			session:    &store.Sessions{CsrfToken: "correct-token"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "POST with valid Origin and valid CSRF token succeeds",
			method:     http.MethodPost,
			origin:     "http://example.com",
			csrfHeader: "correct-token",
			session:    &store.Sessions{CsrfToken: "correct-token"},
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := gin.New()
			r.Use(func(c *gin.Context) {
				if tt.session != nil {
					c.Set(middleware.ContextSession, *tt.session)
				}
				c.Next()
			})
			r.Use(middleware.RequireCSRF())
			r.Any("/test", func(c *gin.Context) {
				c.Status(http.StatusOK)
			})

			req, _ := http.NewRequestWithContext(context.Background(), tt.method, "http://example.com/test", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.referer != "" {
				req.Header.Set("Referer", tt.referer)
			}
			if tt.csrfHeader != "" {
				req.Header.Set(middleware.CSRFHeader, tt.csrfHeader)
			}

			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("expected status %d, got %d, body: %s", tt.wantStatus, w.Code, w.Body.String())
			}
		})
	}
}

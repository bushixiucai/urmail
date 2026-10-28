package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
)

func TestBrandAndUnknownHostsCannotUseGlobalLegacyOrMintCredentials(t *testing.T) {
	for _, tc := range []struct {
		name, host string
		branded    bool
	}{
		{"brand", "brand.example.test", true},
		{"unknown", "unknown.example.test", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := gin.New()
			router.Use(middleware.BrandHostBoundary(nil, "panel.example.test"))
			router.Use(func(c *gin.Context) {
				if tc.branded {
					c.Set(middleware.ContextBrandID, int64(7))
				}
				c.Set(middleware.ContextUserID, int64(1))
			})
			router.GET("/legacy", (&LegacyHandler{}).Status)
			router.GET("/platform", middleware.RequirePlatformAdmin(nil), func(_ *gin.Context) { t.Fatal("operator boundary reached protected operation") })
			router.POST("/mint", (&AuthHandler{}).CreateLegacySession)
			for _, endpoint := range []string{"/legacy", "/platform", "/mint"} {
				method := http.MethodGet
				if endpoint == "/mint" {
					method = http.MethodPost
				}
				req := httptest.NewRequestWithContext(t.Context(), method, "https://"+tc.host+endpoint, strings.NewReader(`{"code":"fresh-proof"}`))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Forwarded-Host", "panel.example.test")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != http.StatusForbidden {
					t.Fatalf("%s %d %s", endpoint, rec.Code, rec.Body.String())
				}
			}
		})
	}
}

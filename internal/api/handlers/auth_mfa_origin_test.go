package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
)

func TestMFAPendingChallengeRejectsChangedOriginAndBrandBeforeSecondFactor(t *testing.T) {
	for _, tc := range []struct {
		name, host string
		brand      int64
	}{
		{"other-host", "https://other.example.test", 5},
		{"other-port", "https://brand.example.test:444", 5},
		{"other-brand", "https://brand.example.test", 6},
		{"unbranded-route", "https://brand.example.test", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetMFAState()
			first, _ := gin.CreateTestContext(httptest.NewRecorder())
			first.Request = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://brand.example.test/login", nil)
			first.Set(middleware.ContextBrandID, int64(5))
			token, err := pendingMFATokens.issueForRequest(first, 71, "192.0.2.1|fixture@example.test", 3)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(map[string]string{"mfa_token": token, "code": "one-time-recovery-proof"})
			router := gin.New()
			router.POST("/verify", func(c *gin.Context) {
				if tc.brand != 0 {
					c.Set(middleware.ContextBrandID, tc.brand)
				}
			}, (&AuthHandler{}).MFAVerify)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, tc.host+"/verify", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Forwarded-Host", "brand.example.test")
			req.Header.Set("Forwarded", "host=brand.example.test;proto=https")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "SESSION_ORIGIN_MISMATCH") {
				t.Fatalf("changed boundary reached second-factor store: %d %s", recorder.Code, recorder.Body.String())
			}
			pendingMFATokens.mu.Lock()
			pending, exists := pendingMFATokens.entries[token]
			pendingMFATokens.mu.Unlock()
			if !exists || pending.OriginHost != "brand.example.test" || pending.BrandID != 5 || !pending.OriginBound {
				t.Fatal("wrong-origin attempt consumed or rewrote the original proof")
			}
		})
	}
}

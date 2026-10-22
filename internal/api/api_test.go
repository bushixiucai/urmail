package api_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api"
	"github.com/bushixiucai/urmail/internal/i18n"
)

func TestHealthzAndOpenRoutes(t *testing.T) {
	logger := zap.NewNop()
	messages, err := i18n.NewStore()
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}

	engine := api.New(api.Options{
		Logger:   logger,
		Messages: messages,
		Version:  "1.0.0",
	})

	// 1. Test /api/v1/healthz
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/healthz", nil)
	engine.ServeHTTP(w, req)

	// Since AppPool is nil in this test, healthz returns 503 degraded
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}

	// 2. Test /api/v1/i18n/zh-CN
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodGet, "/api/v1/i18n/zh-CN", nil)
	engine.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w2.Code)
	}

	// 3. Test Protected Route without token -> 401 Unauthorized
	w3 := httptest.NewRecorder()
	req3, _ := http.NewRequest(http.MethodGet, "/api/v1/domains", nil)
	engine.ServeHTTP(w3, req3)
	if w3.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w3.Code)
	}

	// 4. Test Protected Route with invalid Bearer -> 401
	w4 := httptest.NewRecorder()
	req4, _ := http.NewRequest(http.MethodGet, "/api/v1/domains", nil)
	req4.Header.Set("Authorization", "Bearer invalid-token")
	engine.ServeHTTP(w4, req4)
	if w4.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid session, got %d, body: %s", w4.Code, w4.Body.String())
	}

	// 5. Test Login with bad JSON payload -> 400
	w5 := httptest.NewRecorder()
	req5, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString("{invalid"))
	req5.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(w5, req5)
	if w5.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w5.Code)
	}
}

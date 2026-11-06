package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestRestoreQuarantineRejectsEveryExternalMutation(t *testing.T) {
	r := New(Options{Logger: zap.NewNop(), Quarantined: true})
	for _, path := range []string{"/api/v1/billing/webhook/stripe", "/api/v1/billing/checkout", "/api/v1/mailbox/ai", "/api/v1/domains/1/verify", "/api/v1/marketing/campaigns/1/start", "/api/v1/platform/refunds/1/approve", "/api/v1/auth/register", "/api/v1/auth/register/verify", "/api/v1/auth/password", "/api/v1/auth/mfa/disable"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(`{}`)))
		if w.Code != 503 || !strings.Contains(w.Body.String(), "RESTORE_QUARANTINED") {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
}

func TestQuarantineHasRequestIdentityLogsAndHealthState(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	r := New(Options{Logger: zap.New(core), Quarantined: true})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{}`)))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("X-Request-ID") == "" || logs.Len() == 0 {
		t.Fatalf("quarantine aborted before observability status%d requestid%s logs%d", w.Code, w.Header().Get("X-Request-ID"), logs.Len())
	}
	health := httptest.NewRecorder()
	r.ServeHTTP(health, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/healthz", nil))
	var state map[string]any
	if json.Unmarshal(health.Body.Bytes(), &state) != nil || state["quarantined"] != true {
		t.Fatalf("quarantine health state missing %s", health.Body)
	}
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/mfa/verify", "/api/v1/auth/logout", "/api/v1/auth/switch-tenant"} {
		allowed := httptest.NewRecorder()
		r.ServeHTTP(allowed, httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(`{}`)))
		if strings.Contains(allowed.Body.String(), "RESTORE_QUARANTINED") {
			t.Fatalf("safe auth exception blocked %s %d", path, allowed.Code)
		}
	}
}

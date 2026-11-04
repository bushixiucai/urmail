package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/db"
)

func TestWorkloadPoolDefaults(t *testing.T) {
	app, platform := &db.Pool{}, &db.Pool{}
	aiApp, aiPlatform := &db.Pool{}, &db.Pool{}
	marketingApp, marketingPlatform := &db.Pool{}, &db.Pool{}
	legacy := workloadPoolDefaults(Options{AppPool: app, PlatformPool: platform})
	if legacy.AIAppPool != app || legacy.MarketingAppPool != app || legacy.AIPlatformPool != platform || legacy.MarketingPlatformPool != platform {
		t.Fatal("legacy callers must inherit their existing pools")
	}
	explicit := workloadPoolDefaults(Options{AppPool: app, PlatformPool: platform, AIAppPool: aiApp, AIPlatformPool: aiPlatform, MarketingAppPool: marketingApp, MarketingPlatformPool: marketingPlatform})
	if explicit.AIAppPool != aiApp || explicit.AIPlatformPool != aiPlatform || explicit.MarketingAppPool != marketingApp || explicit.MarketingPlatformPool != marketingPlatform {
		t.Fatal("explicit isolated pools must never be replaced")
	}
	if explicit.AppPool != app || explicit.PlatformPool != platform {
		t.Fatal("core pools changed")
	}
	empty := workloadPoolDefaults(Options{})
	if empty.AIAppPool != nil || empty.AIPlatformPool != nil || empty.MarketingAppPool != nil || empty.MarketingPlatformPool != nil {
		t.Fatal("nil options must remain usable")
	}
}

// These requests exercise the real route registrations and authentication gates.
// They do not claim database saturation, RLS, or network-probe acceptance.
func TestWorkloadAndProbeRoutesRemainAuthenticated(t *testing.T) {
	engine := New(Options{Logger: zap.NewNop()})
	routes := []struct{ method, path string }{
		{"POST", "/api/v1/domains/1/verify"},
		{"POST", "/api/v1/ai/health"},
		{"GET", "/api/v1/ai/configuration"},
		{"PUT", "/api/v1/ai/configuration"},
		{"GET", "/api/v1/ai/usage"},
		{"POST", "/api/v1/ai/consent"},
		{"GET", "/api/v1/ai/budget"},
		{"GET", "/api/v1/marketing/channels"},
		{"POST", "/api/v1/marketing/templates"},
		{"GET", "/api/v1/marketing/campaigns/1/preview"},
		{"GET", "/api/v1/marketing/campaigns/1/report"},
		{"POST", "/api/v1/marketing/suppressions"},
		{"GET", "/api/v1/mailbox/ai/target"},
		{"POST", "/api/v1/mailbox/ai/assist"},
		{"POST", "/api/v1/mailbox/ai/search-ask"},
		{"GET", "/api/v1/mailbox/folders"},
	}
	for _, route := range routes {
		t.Run(route.method+route.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), route.method, route.path, nil))
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d; want authenticated route (401); body=%s", response.Code, response.Body.String())
			}
		})
	}
}

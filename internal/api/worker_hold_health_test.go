package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestHealthDisclosesWorkerHoldWithoutHidingDatabaseFailure(t *testing.T) {
	for _, held := range []bool{false, true} {
		router := gin.New()
		router.GET("/health", healthz(Options{WorkersHeld: held, Version: "test"}))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), "GET", "/health", nil))
		var body struct {
			Status string `json:"status"`
			DB     string `json:"db"`
			Held   *bool  `json:"workers_held"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if response.Code != 503 || body.Status != "degraded" || body.DB != "unavailable" || body.Held == nil || *body.Held != held {
			t.Fatalf("health hid maintenance or failed dependency: %s", response.Body.String())
		}
	}
}

func TestProductionRouteTreeAndSieveGateReadinessPG(t *testing.T) {
	platform, app := testdb.OpenMigrated(t)
	for _, ready := range []bool{false, true} {
		// Construct the complete production route tree, including independently
		// registered recurring and legacy merchant routes. Wildcard collisions
		// must fail this test rather than panic only after deployment.
		router := New(Options{PlatformPool: platform, AppPool: app, WorkersHeld: true, SieveGateReady: func() bool { return ready }})
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/healthz", nil))
		var body struct {
			Status string `json:"status"`
			Ready  bool   `json:"sieve_outbound_gate_ready"`
			Held   bool   `json:"workers_held"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		status := 503
		if ready {
			status = 200
		}
		if response.Code != status || body.Ready != ready || !body.Held {
			t.Fatalf("lost outbound protection was not observable: %d %s", response.Code, response.Body.String())
		}
	}
}

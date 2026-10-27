package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// readinessSettingKey mirrors internal/telemetry's published settings key.
const readinessSettingKey = "observability_readiness"

// StatusReadyQuerier executes one raw read against the platform database.
// A raw query keeps this read-only view decoupled from the telemetry
// package's sqlc surface.
type StatusReadyQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// StatusReady serves the evaluator's last structured readiness report. It
// never changes healthz semantics: process liveness, mail readiness and
// configuration convergence stay separate facts, and a missing report means
// "not evaluated yet", never "ready".
// GET /api/v1/internal/status/ready
func StatusReady(c *gin.Context) {
	store, ok := c.MustGet("statusReadyQuerier").(StatusReadyQuerier)
	if !ok || store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "READINESS_UNAVAILABLE"})
		return
	}
	var raw string
	err := store.QueryRow(c.Request.Context(),
		"SELECT value FROM settings WHERE scope='platform' AND scope_id=0 AND key=$1",
		readinessSettingKey).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusOK, gin.H{"evaluated": false})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "READINESS_UNAVAILABLE"})
		return
	}
	var report struct {
		ProcessUp       bool      `json:"process_up"`
		MailReady       bool      `json:"mail_ready"`
		ConfigConverged bool      `json:"config_converged"`
		GeneratedAt     time.Time `json:"generated_at"`
		FiringAlerts    []string  `json:"firing_alerts"`
	}
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "READINESS_UNAVAILABLE"})
		return
	}
	if report.FiringAlerts == nil {
		report.FiringAlerts = []string{}
	}
	c.JSON(http.StatusOK, gin.H{"evaluated": true, "report": report})
}

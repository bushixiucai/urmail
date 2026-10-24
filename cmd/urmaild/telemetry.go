package main

import (
	"context"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/agent"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/telemetry"
)

// startMailTelemetry reads only through the typed root-agent capability. The
// management daemon never receives broad adm-group access to system logs.
func startMailTelemetry(ctx context.Context, cfg *config.Config, pool *db.Pool, logger *zap.Logger) {
	if cfg.DevMode || cfg.Quarantined {
		return
	}
	stream := cfg.MyHostname
	if stream == "" {
		stream = "local"
	}
	collector := &telemetry.Collector{Pool: pool, Stream: stream, Source: telemetry.AgentSource{Client: agent.NewClient(cfg.AgentSocket)}}
	go collector.Start(ctx, logger)
}

// startMailObservability remains read-only toward host services.
func startMailObservability(ctx context.Context, cfg *config.Config, pool *db.Pool, logger *zap.Logger) {
	if cfg.DevMode || cfg.Quarantined {
		return
	}
	stream := cfg.MyHostname
	if stream == "" {
		stream = "local"
	}
	evaluator := &telemetry.Evaluator{Pool: pool, Stream: stream, Source: telemetry.RuntimeObserver{Client: agent.NewClient(cfg.AgentSocket)}}
	go evaluator.Start(ctx, logger)
}

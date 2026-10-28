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
	if !cfg.BackgroundWorkersAllowed() {
		return
	}
	stream := cfg.MyHostname
	if stream == "" {
		stream = "local"
	}
	collector := &telemetry.Collector{Pool: pool, Stream: stream, Source: telemetry.AgentSource{Client: agent.NewClient(cfg.AgentSocket)}}
	go collector.Start(ctx, logger)
}

// mailCertificateProbeAddress returns the loopback HTTPS endpoint for the
// OPS-003 certificate expiry fallback probe; empty when no hostname is known.
func mailCertificateProbeAddress(cfg *config.Config) string {
	if cfg.MyHostname == "" {
		return ""
	}
	return "127.0.0.1:443"
}

// startMailObservability remains read-only toward host services.
func startMailObservability(ctx context.Context, cfg *config.Config, pool *db.Pool, logger *zap.Logger) {
	if !cfg.BackgroundWorkersAllowed() {
		return
	}
	stream := cfg.MyHostname
	if stream == "" {
		stream = "local"
	}
	evaluator := &telemetry.Evaluator{Pool: pool, Stream: stream, Source: telemetry.RuntimeObserver{Client: agent.NewClient(cfg.AgentSocket)}}
	evaluator.CertificateAddress = mailCertificateProbeAddress(cfg)
	go evaluator.Start(ctx, logger)
}

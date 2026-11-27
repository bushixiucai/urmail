package main

import (
	"context"
	"database/sql"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/agent"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/dovecotauth"
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

// sqliteDovecotAuthSource sources the auth snapshot from the single-engine
// SQLite store; database/sql.Rows satisfies the dovecotauth.Rows contract.
type sqliteDovecotAuthSource struct{ db *sql.DB }

func (s sqliteDovecotAuthSource) Query(ctx context.Context, query string, args ...any) (dovecotauth.Rows, error) {
	return s.db.QueryContext(ctx, query, args...)
}

type dovecotAuthSource struct{ pool *db.Pool }

func (s dovecotAuthSource) Query(ctx context.Context, query string, args ...any) (dovecotauth.Rows, error) {
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return dovecotAuthRows{Rows: rows}, nil
}

type dovecotAuthRows struct{ pgx.Rows }

func (r dovecotAuthRows) Close() error {
	r.Rows.Close()
	return nil
}

// startDovecotAuthSnapshot binds the read-only SQLite dovecot auth snapshot
// manager, runs startup sampling reconciliation, and schedules the 24h fallback rebuild.
// The builder issues a dialect-neutral SELECT set, so both the PostgreSQL
// platform pool and the single-engine SQLite store can source it
// (cutover-blockers: the slice-1 branch previously returned before this
// wiring, leaving the snapshot manager dead and dovecot auth on a stale
// file).
func startDovecotAuthSnapshot(ctx context.Context, cfg *config.Config, src dovecotauth.Source, logger *zap.Logger) {
	if !cfg.IsSQLite() || cfg.DovecotAuthDB == "" || src == nil {
		return
	}
	mgr := dovecotauth.NewManager(cfg.DovecotAuthDB, src)
	dovecotauth.SetMetricsManager(mgr)
	if err := mgr.Reconcile(ctx, 20); err != nil {
		logger.Warn("dovecot auth snapshot reconciliation triggered repair", zap.Error(err))
		if _, repairErr := mgr.Repair(ctx); repairErr != nil {
			logger.Warn("dovecot auth snapshot initial repair failed", zap.Error(repairErr))
		}
	}
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := mgr.Rebuild(ctx); err != nil {
					logger.Warn("dovecot auth snapshot 24h fallback rebuild failed", zap.Error(err))
				}
			}
		}
	}()
}

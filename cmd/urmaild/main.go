// Command urmaild is the urMail.cn management service.
//
// It serves the management API and the embedded web interface on loopback.
// Privileged work is delegated to urmaild-agent over a unix socket.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/logging"
	"github.com/bushixiucai/urmail/internal/store"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "urmaild: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger, err := logging.New(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("build logger: %w", err)
	}
	defer func() { _ = logger.Sync() }()

	logger.Info("starting urmaild",
		zap.String("version", version),
		zap.String("listen", cfg.ListenAddr),
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	appPool, err := db.OpenApp(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open tenant database pool: %w", err)
	}
	defer appPool.Close()

	platformPool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return fmt.Errorf("open platform database pool: %w", err)
	}
	defer platformPool.Close()

	messages, err := i18n.NewStore()
	if err != nil {
		return fmt.Errorf("load message catalogue: %w", err)
	}

	// Periodically purge expired sessions so the sessions table does not
	// grow without bound. Expired sessions are already rejected at lookup
	// time; this is pure housekeeping.
	go purgeExpiredSessions(ctx, appPool, logger)

	engine := api.New(api.Options{
		Logger:       logger,
		AppPool:      appPool,
		PlatformPool: platformPool,
		Messages:     messages,
		Version:      version,
	})

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           engine,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", zap.String("addr", cfg.ListenAddr))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	return nil
}

// purgeExpiredSessions deletes expired sessions once an hour until ctx is
// cancelled.
func purgeExpiredSessions(ctx context.Context, pool *db.Pool, logger *zap.Logger) {
	queries := store.New(pool.Pool)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := queries.DeleteExpiredSessions(ctx); err != nil {
				logger.Warn("purge expired sessions", zap.Error(err))
			}
		}
	}
}

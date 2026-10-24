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

	"github.com/bushixiucai/urmail/internal/agent"
	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/api"
	"github.com/bushixiucai/urmail/internal/api/handlers"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/dnsprovider"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/logging"
	"github.com/bushixiucai/urmail/internal/mail/policy"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/reconcile"
	"github.com/bushixiucai/urmail/internal/secrets"
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
		return fmt.Errorf("load message catalog: %w", err)
	}

	var dnsProvider dnsprovider.Provider
	switch cfg.DNSProvider {
	case "cloudflare":
		if cfg.CloudflareToken != "" {
			dnsProvider = dnsprovider.NewCloudflare(cfg.CloudflareToken)
		}
	case "dnspod":
		if cfg.DNSPodToken != "" {
			dnsProvider = dnsprovider.NewDNSPod(cfg.DNSPodToken)
		}
	case "aliyun":
		if cfg.AliyunAccessKeyID != "" && cfg.AliyunAccessKeySecret != "" {
			dnsProvider = dnsprovider.NewAliyun(cfg.AliyunAccessKeyID, cfg.AliyunAccessKeySecret)
		}
	}
	var stripe *payment.Stripe
	if cfg.StripeSecretKey != "" {
		stripe = payment.NewStripe(cfg.StripeSecretKey, cfg.StripeWebhookSecret)
	}
	baseURL := "https://" + cfg.MyHostname
	if cfg.MyHostname == "" {
		baseURL = "https://box." + cfg.PrimaryDomain
	}

	var secretBox handlers.SecretsBox
	if cfg.MasterKeyPath != "" {
		if box, loadErr := secrets.Load(cfg.MasterKeyPath); loadErr != nil {
			logger.Warn("master key unavailable; TOTP secrets stay legacy plaintext", zap.Error(loadErr))
		} else {
			secretBox = box
		}
	}

	// Periodically purge expired sessions so the sessions table does not
	// grow without bound. Expired sessions are already rejected at lookup
	// time; this is pure housekeeping. The app role has no access to
	// sessions (0008), so the platform pool performs the delete.
	go purgeExpiredSessions(ctx, platformPool, logger)

	// The Postfix submission/smtps services ask this socket whether an
	// authenticated sender is still within their plan's daily send limit.
	if cfg.PolicyEnabled {
		svc := policy.NewService()
		svc.InboundChecker = &policy.DBInboundChecker{Store: store.New(platformPool.Pool)}
		svc.RateLimiter = policy.NewRateLimiter(300, time.Minute)
		svc.QuotaChecker = policy.NewAtomicDBQuotaChecker(
			store.New(platformPool.Pool), platformPool.Pool, 0,
		)
		policyServer := policy.NewServer(cfg.PolicySocket, svc)
		go func() {
			logger.Info("policy service listening", zap.String("socket", cfg.PolicySocket))
			if err := policyServer.Start(ctx); err != nil && ctx.Err() == nil {
				logger.Error("policy service stopped", zap.Error(err))
			}
		}()
	}

	// The reconcile pipeline re-renders all configuration files whenever a
	// domain or mailbox changes and pushes them through the privileged
	// agent, followed by the affected service reloads.
	reconcilePipeline := reconcile.NewPipeline(cfg, &reconcile.VersionedStore{StoreQuerier: store.New(platformPool.Pool), DB: platformPool.Pool}, cfg.PublicIPv4, cfg.Nameservers).WithLogger(logger)
	go reconcilePipeline.StartWorker(ctx, 2*time.Second, func(files map[string]string) error {
		if cfg.DevMode {
			logger.Info("development configuration rendered without privileged apply", zap.Int("files", len(files)))
			return nil
		}
		snapshot := reconcilePipeline.CurrentSnapshot()
		if snapshot == nil {
			return fmt.Errorf("configuration snapshot unavailable")
		}
		_, err := agent.NewClient(cfg.AgentSocket).Do(ctx, agentd.OpConfigApply, agentd.ApplyParams{Postfix: snapshot.PostfixSnap, DNS: snapshot.NSDSnap, DKIM: snapshot.DKIM})
		return err
	})

	// Render once on startup so a fresh install does not keep the stock
	// distribution configuration until the first tenant write.
	reconcilePipeline.Trigger()

	var recallClient handlers.RecallExpunger
	if !cfg.DevMode {
		recallClient = agent.NewClient(cfg.AgentSocket)
	}
	engine := api.New(api.Options{
		Recall:          recallClient,
		DevMode:         cfg.DevMode,
		Logger:          logger,
		AppPool:         appPool,
		PlatformPool:    platformPool,
		Messages:        messages,
		Version:         version,
		MyHostname:      cfg.MyHostname,
		Reconcile:       reconcilePipeline,
		Audit:           store.New(platformPool.Pool),
		Secrets:         secretBox,
		DNSProvider:     dnsProvider,
		DNSManagedZones: cfg.DNSManagedZones,
		Payment:         stripe,
		QuoteHMACKey:    cfg.QuoteHMACKey,
		BaseURL:         baseURL,
		MarketingTokens: marketingTokenManager(cfg),
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
// canceled.
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

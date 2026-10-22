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
	"sort"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/agent"
	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/api"
	"github.com/bushixiucai/urmail/internal/api/handlers"
	"github.com/bushixiucai/urmail/internal/certs"
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
		return fmt.Errorf("load message catalogue: %w", err)
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
	reconcilePipeline := reconcile.NewPipeline(cfg, store.New(platformPool.Pool), cfg.PublicIPv4, cfg.Nameservers).WithLogger(logger)
	reconcilePipeline.WithCertificateEnsurer(func(ctx context.Context) error {
		return ensureDomainCertificates(ctx, store.New(platformPool.Pool), cfg.MasterKeyPath, logger)
	})
	go reconcilePipeline.StartWorker(ctx, 2*time.Second, func(files map[string]string) error {
		return pushRenderedConfigs(ctx, cfg.AgentSocket, files, logger)
	})
	// Render once on startup so a fresh install does not keep the stock
	// distribution configuration until the first tenant write.
	reconcilePipeline.Trigger()

	engine := api.New(api.Options{
		Logger:       logger,
		AppPool:      appPool,
		PlatformPool: platformPool,
		Messages:     messages,
		Version:      version,
		MyHostname:   cfg.MyHostname,
		Reconcile:    reconcilePipeline,
		Audit:        store.New(platformPool.Pool),
		Secrets:      secretBox,
		DNSProvider:  dnsProvider,
		Payment:      stripe,
		BaseURL:      baseURL,
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

// ensureDomainCertificates creates a self-signed placeholder for every active
// domain so nginx has a valid certificate while ACME is pending. ACME
// certificates at the same paths are never overwritten.
func ensureDomainCertificates(ctx context.Context, q *store.Queries, masterKeyPath string, logger *zap.Logger) error {
	domains, err := q.PlatformListDomains(ctx)
	if err != nil {
		return fmt.Errorf("list domains for certificates: %w", err)
	}
	for _, d := range domains {
		if d.Status != "active" {
			continue
		}
		if _, _, err := certs.EnsureDomainCertificate(masterKeyPath, d.Fqdn); err != nil {
			logger.Warn("ensure domain certificate", zap.String("domain", d.Fqdn), zap.Error(err))
		}
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

// reloadUnits are the services affected by rendered configuration files.
// dovecot must be up before postfix: smtpd needs the SASL auth socket at
// startup, otherwise submission fails with "no SASL authentication mechanisms".
var reloadUnits = []string{"opendkim", "dovecot", "postfix", "nginx", "nsd"}

// pushRenderedConfigs writes every rendered file through the privileged
// agent and restarts the mail services. Any failure aborts the push so the
// next trigger retries the whole cycle; renders are deterministic.
func pushRenderedConfigs(ctx context.Context, agentSocket string, files map[string]string, logger *zap.Logger) error {
	client := agent.NewClient(agentSocket)

	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		if _, err := client.Do(ctx, "file.write", agentd.WriteFileParams{
			Path:    path,
			Content: files[path],
		}); err != nil {
			return fmt.Errorf("write %s via agent: %w", path, err)
		}
	}

	// Postfix hash: maps are not usable until postmap has built the .db file.
	// Compile every map after all files are in place and before restarting
	// Postfix, so a failed map build never leaves the daemon running with a
	// half-written table.
	for _, path := range paths {
		if !strings.HasSuffix(path, ".map") {
			continue
		}
		if _, err := client.Do(ctx, agentd.OpPostmap, agentd.PostmapParams{Path: path}); err != nil {
			return fmt.Errorf("postmap %s via agent: %w", path, err)
		}
	}

	for _, unit := range reloadUnits {
		if _, err := client.Do(ctx, "systemctl.restart", agentd.RestartUnitParams{Unit: unit}); err != nil {
			return fmt.Errorf("restart %s via agent: %w", unit, err)
		}
	}
	logger.Info("rendered configuration pushed", zap.Int("files", len(files)))
	return nil
}

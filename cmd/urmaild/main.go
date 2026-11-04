// Command urmaild is the urMail.cn management service.
//
// It serves the management API and the embedded web interface on loopback.
// Privileged work is delegated to urmaild-agent over a unix socket.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
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
	archiveStore "github.com/bushixiucai/urmail/internal/archive"
	"github.com/bushixiucai/urmail/internal/billingrenew"
	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/dnsprovider"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/logging"
	"github.com/bushixiucai/urmail/internal/mail/frombind"
	"github.com/bushixiucai/urmail/internal/mail/frommilter"
	"github.com/bushixiucai/urmail/internal/mail/policy"
	"github.com/bushixiucai/urmail/internal/mail/postfix"
	"github.com/bushixiucai/urmail/internal/mailauth"
	"github.com/bushixiucai/urmail/internal/mailboxschedule"
	"github.com/bushixiucai/urmail/internal/mailspam"
	"github.com/bushixiucai/urmail/internal/marketing"
	"github.com/bushixiucai/urmail/internal/notify"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/reconcile"
	refundService "github.com/bushixiucai/urmail/internal/refund"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		if len(os.Args) == 2 {
			switch os.Args[1] {
			case "--version", "-version":
				fmt.Printf("urmaild %s\n", version)
				return
			case "--help", "-h":
				fmt.Println("Usage: urmaild [--version|--help] (configuration is read from the environment)")
				return
			}
		}
		fmt.Fprintln(os.Stderr, "urmaild: unexpected arguments; use --help")
		os.Exit(2)
	}
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
	if cfg.DNSSecondaryOnly {
		return fmt.Errorf("DNS-only secondary configuration is only accepted by urmaild-agent")
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

	// Validate the encryption authority before opening databases or starting any
	// listener or worker. A missing key must never enable plaintext secret writes.
	secretBox, err := loadRuntimeSecrets(cfg)
	if err != nil {
		return err
	}

	appPool, err := db.OpenBudgeted(ctx, cfg.DatabaseURL, db.RoleApp, "core-app", cfg.DatabaseBudgets.Core)
	if err != nil {
		return fmt.Errorf("open tenant database pool: %w", err)
	}
	defer appPool.Close()

	platformPool, err := db.OpenBudgeted(ctx, cfg.DatabaseURLPlatform, db.RolePlatform, "core-platform", cfg.DatabaseBudgets.Core)
	if err != nil {
		return fmt.Errorf("open platform database pool: %w", err)
	}
	defer platformPool.Close()

	// Seal legacy recovery MIME before any worker can read it, including on a
	// quarantined restore where delivery workers deliberately remain stopped.
	if secretBox != nil {
		if err := mailboxschedule.SealLegacyCopies(ctx, platformPool, secretBox); err != nil {
			return fmt.Errorf("seal legacy webmail recovery material: %w", err)
		}
	}

	marketingPool, err := db.OpenBudgeted(ctx, cfg.DatabaseURLPlatform, db.RolePlatform, "marketing-platform", cfg.DatabaseBudgets.Marketing)
	if err != nil {
		return fmt.Errorf("open marketing database pool: %w", err)
	}
	defer marketingPool.Close()
	backgroundPool, err := db.OpenBudgeted(ctx, cfg.DatabaseURLPlatform, db.RolePlatform, "background", cfg.DatabaseBudgets.Background)
	if err != nil {
		return fmt.Errorf("open background database pool: %w", err)
	}
	defer backgroundPool.Close()
	aiAppPool, err := db.OpenBudgeted(ctx, cfg.DatabaseURL, db.RoleApp, "ai-app", cfg.DatabaseBudgets.AI)
	if err != nil {
		return fmt.Errorf("open AI tenant pool: %w", err)
	}
	defer aiAppPool.Close()
	aiPlatformPool, err := db.OpenBudgeted(ctx, cfg.DatabaseURLPlatform, db.RolePlatform, "ai-platform", cfg.DatabaseBudgets.AI)
	if err != nil {
		return fmt.Errorf("open AI platform pool: %w", err)
	}
	defer aiPlatformPool.Close()
	marketingAppPool, err := db.OpenBudgeted(ctx, cfg.DatabaseURL, db.RoleApp, "marketing-app", cfg.DatabaseBudgets.Marketing)
	if err != nil {
		return fmt.Errorf("open marketing tenant pool: %w", err)
	}
	defer marketingAppPool.Close()
	// Cancel workers before pool cleanup also on startup/server failure, not only signals.
	defer stop()

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

	// Periodically purge expired sessions so the sessions table does not
	// grow without bound. Expired sessions are already rejected at lookup
	// time; this is pure housekeeping. The app role has no access to
	// sessions (0008), so the platform pool performs the delete.
	go purgeExpiredSessions(ctx, backgroundPool, logger)
	go handlers.RunMFAReaper(ctx)
	startBackgroundWorker(ctx, cfg, func(ctx context.Context) { payment.RunWebhookMaintenance(ctx, backgroundPool, logger) })
	startBackgroundWorker(ctx, cfg, func(ctx context.Context) { notify.Run(ctx, backgroundPool, logger) })
	notificationWorker := &notify.Worker{Pool: backgroundPool, Secrets: secretBox, Messages: messages, FallbackLocale: cfg.Locale, BaseURL: baseURL, Enabled: cfg.NotificationsEnabled, DevMode: cfg.DevMode, Quarantined: cfg.Quarantined}
	startBackgroundWorker(ctx, cfg, func(ctx context.Context) { notificationWorker.Run(ctx, logger) })
	renewalRegistry := &payment.Registry{Store: store.New(backgroundPool), Secrets: secretBox, BaseURL: baseURL}
	renewalDispatcher := &billingrenew.Dispatcher{Pool: backgroundPool, Registry: renewalRegistry, Secrets: secretBox, Adapters: billingrenew.NewProductionAdapters(renewalRegistry)}
	renewals := billingrenew.Worker{Store: store.New(backgroundPool), Chargers: renewalDispatcher, Coordinator: billingrenew.PoolCoordinator{Pool: backgroundPool}, Settlements: billingrenew.PoolSettlementStore{Pool: backgroundPool}}
	startRenewalWorker(ctx, cfg, renewals.RunOnce, logger)
	reminderWorker := appcollab.ReminderWorker{Pool: backgroundPool, Logger: logger}
	startPeriodicWorker(ctx, cfg, time.Minute, "calendar reminder pass failed", reminderWorker.RunOnce, logger)

	var sieveGateReady func() bool
	if !cfg.DevMode && !cfg.Quarantined {
		sieveGateReady, err = startSieveGate(ctx, platformPool, logger)
		if err != nil {
			return fmt.Errorf("start personal Sieve entitlement gate: %w", err)
		}
	}

	// Complete automatic backups run in the independent root-owned systemd
	// service, outside the daemon and agent cgroups paused by maintenance.

	// MAIL-04: Postfix submission/smtps consult this From-authorization
	// milter before accepting a message; it fails closed (submission-side
	// tempfail) when unavailable. Port 25 inbound is deliberately not gated
	// on it.
	listenConfig := net.ListenConfig{}
	frombindListener, frombindErr := listenConfig.Listen(ctx, "tcp", "127.0.0.1:12302")
	if frombindErr != nil {
		logger.Error("frombind milter listen failed; authenticated submissions will tempfail", zap.Error(frombindErr))
	} else {
		authorizer := frombind.MapFileAuthorizer{
			Path: "/etc/postfix/" + postfix.FileSenderLoginMap,
		}
		go func() {
			logger.Info("frombind milter listening", zap.String("addr", "127.0.0.1:12302"))
			// The listener closes with ctx, so the final Serve error is the
			// expected shutdown signal and is logged at info level.
			logger.Info("frombind milter stopped", zap.Error(frommilter.Serve(frombindListener, authorizer)))
		}()
	}

	// The Postfix submission/smtps services ask this socket whether an
	// authenticated sender is still within their plan's daily send limit.
	if cfg.PolicyEnabled && !cfg.Quarantined {
		svc := policy.NewService()
		svc.InboundChecker = &policy.DBInboundChecker{Store: store.New(platformPool)}
		svc.RateLimiter = policy.NewRateLimiter(300, time.Minute)
		svc.QuotaChecker = policy.NewAtomicDBQuotaChecker(
			store.New(platformPool), platformPool.Pool, 0,
		)
		policyServer := policy.NewServer(cfg.PolicySocket, svc)
		go func() {
			logger.Info("policy service listening", zap.String("socket", cfg.PolicySocket))
			if err := policyServer.Start(ctx); err != nil && ctx.Err() == nil {
				logger.Error("policy service stopped", zap.Error(err))
			}
		}()
	}

	// MAIL-002: spam verdict milter on loopback. The generated Postfix
	// smtpd_milters chain references inet:127.0.0.1:11339 (after the
	// DKIM/DMARC milters); main.cf keeps milter_default_action=accept, which
	// is the documented degrade path, and unavailability raises the
	// spam_milter_unavailable alert instead of blocking mail.
	if !cfg.Quarantined {
		spamVerdict := &mailspam.Engine{
			Platform: platformPool,
			App:      appPool,
			Config: mailspam.EngineConfig{
				// OpenDKIM/OpenDMARC sign Authentication-Results with the
				// MTA hostname as authserv-id; anything else is untrusted.
				TrustedAuthServIDs: []string{cfg.MyHostname},
			},
		}
		spamMilter := &mailspam.Server{Addr: mailspam.DefaultAddr, Handler: spamVerdict, Config: mailspam.MilterConfig{Authentication: &mailauth.Receiver{Secrets: secretBox}}}
		go func() {
			logger.Info("spam verdict milter listening", zap.String("addr", mailspam.DefaultAddr))
			if err := spamMilter.ListenAndServe(ctx); err != nil && ctx.Err() == nil {
				logger.Error("spam verdict milter stopped", zap.Error(err))
			}
		}()
	}

	// The reconcile pipeline re-renders all configuration files whenever a
	// domain or mailbox changes and pushes them through the privileged
	// agent, followed by the affected service reloads.
	agentClient := agent.NewClient(cfg.AgentSocket)
	applyFn := func(applyCtx context.Context, params reconcile.ApplyParams) (string, error) {
		if cfg.DevMode {
			logger.Info("development configuration rendered without privileged apply", zap.Int("domains", len(params.Postfix.Domains)))
			return "dev-mode", nil
		}
		resp, err := agentClient.Do(applyCtx, agentd.OpConfigApply, params)
		if err != nil {
			return "", err
		}
		return resp.Stdout, nil
	}
	reconcilePipeline := reconcile.NewPipeline(cfg, &reconcile.VersionedStore{StoreQuerier: store.New(platformPool), DB: platformPool.Pool}, cfg.PublicIPv4, cfg.Nameservers).
		WithLogger(logger).
		WithApplier(applyFn)
	if !cfg.Quarantined {
		go reconcilePipeline.StartWorker(ctx, 2*time.Second, nil)

		// Render once on startup so a fresh install does not keep the stock
		// distribution configuration until the first tenant write.
		reconcilePipeline.Trigger()
		startDNSReconcileRefresh(ctx, cfg, reconcilePipeline)
	}

	if cfg.BackgroundWorkersAllowed() {
		archiveWorker := &archiveStore.Worker{Pool: backgroundPool}
		go archiveWorker.Start(ctx, logger)
	}
	dnsPlans := startDomainOnboarding(ctx, cfg, platformPool, secretBox, reconcilePipeline, logger)
	startMailTelemetry(ctx, cfg, backgroundPool, logger)
	startMailObservability(ctx, cfg, backgroundPool, logger)
	startPaymentRecovery(ctx, cfg, backgroundPool, secretBox, baseURL, stripe, logger)
	refunds := &refundService.Service{Pool: backgroundPool, Registry: &payment.Registry{Store: store.New(backgroundPool), Secrets: secretBox, BaseURL: baseURL}, LegacyStripe: stripe, Enabled: cfg.RefundsEnabled, DevMode: cfg.DevMode, Quarantined: cfg.Quarantined}
	startBackgroundWorker(ctx, cfg, func(ctx context.Context) { refunds.Run(ctx, logger) })
	tokenVerifier := marketingTokenManager(cfg)
	tokenManager, _ := tokenVerifier.(*domainMail.SuppressionManager)
	marketingWorker, workerErr := marketing.NewWorker(marketing.Options{Pool: marketingPool, Secrets: secretBox, Tokens: tokenManager, Enabled: cfg.MarketingEnabled && cfg.BackgroundWorkersAllowed(), DevMode: cfg.DevMode, Quarantined: cfg.Quarantined,
		Hostname: cfg.MyHostname, UnsubscribeURL: baseURL + "/api/v1/marketing/unsubscribe", Logger: logger})
	if workerErr != nil {
		return fmt.Errorf("configure marketing worker: %w", workerErr)
	}
	startBackgroundWorker(ctx, cfg, marketingWorker.Run)
	flowWorker, flowWorkerErr := marketing.NewFlowWorker(marketing.FlowOptions{
		Pool:    marketingPool,
		Sender:  (&marketing.FlowSender{Pool: marketingPool, Secrets: secretBox, Tokens: tokenManager, Hostname: cfg.MyHostname, UnsubscribeURL: baseURL + "/api/v1/marketing/unsubscribe", Content: marketing.TemplateFlowContent(marketingPool)}).Send,
		Enabled: cfg.MarketingEnabled && cfg.BackgroundWorkersAllowed(), DevMode: cfg.DevMode, Quarantined: cfg.Quarantined, Logger: logger,
	})
	if flowWorkerErr != nil {
		return fmt.Errorf("configure marketing flow worker: %w", flowWorkerErr)
	}
	startBackgroundWorker(ctx, cfg, flowWorker.Run)
	webmailVault := webmail.NewMemoryVault()
	go webmailVault.Run(ctx, handlers.StoreMailboxNative{Q: store.New(backgroundPool)}.Get)
	smtpAddr := "127.0.0.1:587"
	if cfg.WebmailNative && cfg.BackgroundWorkersAllowed() && secretBox != nil {
		schedWorker := mailboxschedule.NewWorker(mailboxschedule.Options{
			Pool:      backgroundPool,
			Secrets:   secretBox,
			Vault:     webmailVault,
			Dialer:    webmail.NetDialer{Addr: cfg.IMAPAddr},
			Submitter: webmail.NetSubmitter{Addr: smtpAddr},
			MaySend:   handlers.MailAllowed(store.New(backgroundPool)),
			Logger:    logger,
		})
		go schedWorker.Run(ctx)
	}
	var queueReader handlers.QueueReader
	telemetryStream := cfg.MyHostname
	if telemetryStream == "" {
		telemetryStream = "local"
	}
	if !cfg.DevMode {
		queueReader = agent.NewClient(cfg.AgentSocket)
	}
	var recallClient handlers.RecallExpunger
	if !cfg.DevMode {
		recallClient = agent.NewClient(cfg.AgentSocket)
	}
	var sieveActivator handlers.SieveActivator
	if !cfg.DevMode && !cfg.Quarantined {
		sieveClient := agent.NewClient(cfg.AgentSocket)
		sieveActivator = sieveClient
		go handlers.RunSieveReconciler(ctx, platformPool, sieveClient, logger)
	}
	engine := api.New(api.Options{
		RoundcubeEnabled:      cfg.RoundcubeEnabled,
		WebmailNative:         cfg.WebmailNative,
		CardDAVEnabled:        &cfg.CardDAVEnabled,
		CalDAVEnabled:         &cfg.CalDAVEnabled,
		EASEnabled:            &cfg.EASEnabled,
		IMAPAddr:              cfg.IMAPAddr,
		SMTPAddr:              smtpAddr,
		WebmailVault:          webmailVault,
		MetricsToken:          cfg.MetricsToken,
		SieveActivator:        sieveActivator,
		AICloudEndpoints:      cfg.AICloudEndpoints,
		AILocalEndpoints:      cfg.AILocalEndpoints,
		RegistrationEnabled:   cfg.RegistrationEnabled,
		Quarantined:           cfg.Quarantined,
		WorkersHeld:           cfg.WorkersHeld,
		SieveGateReady:        sieveGateReady,
		Recurring:             renewalDispatcher,
		DNSAuthorityAgent:     agent.NewClient(cfg.AgentSocket),
		Recall:                recallClient,
		Queue:                 queueReader,
		TelemetryStream:       telemetryStream,
		DevMode:               cfg.DevMode,
		Logger:                logger,
		AppPool:               appPool,
		PlatformPool:          platformPool,
		AIAppPool:             aiAppPool,
		AIPlatformPool:        aiPlatformPool,
		MarketingAppPool:      marketingAppPool,
		MarketingPlatformPool: marketingPool,
		Messages:              messages,
		Version:               version,
		MyHostname:            cfg.MyHostname,
		Reconcile:             reconcilePipeline,
		Audit:                 store.New(platformPool),
		Secrets:               secretBox,
		DNSProvider:           dnsProvider,
		DNSManagedZones:       cfg.DNSManagedZones,
		DNSPlans:              dnsPlans,
		Payment:               stripe,
		QuoteHMACKey:          cfg.QuoteHMACKey,
		BaseURL:               baseURL,
		MarketingTokens:       tokenVerifier,
		MarketingEnabled:      cfg.MarketingEnabled && !cfg.WorkersHeld,
		RoundcubeBridgeKey:    cfg.RoundcubeBridgeKey,
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

func loadRuntimeSecrets(cfg *config.Config) (handlers.SecretsBox, error) {
	box, err := secrets.Load(cfg.MasterKeyPath)
	if err != nil {
		if cfg.DevMode {
			return nil, nil //nolint:nilnil // Explicit development fixtures may omit secrets; production always fails closed.
		}
		return nil, errors.New("production requires a readable, valid master key; restore the original key before starting urmaild")
	}
	return box, nil
}

// purgeExpiredSessions deletes expired sessions once an hour until ctx is
// canceled.
func purgeExpiredSessions(ctx context.Context, pool *db.Pool, logger *zap.Logger) {
	queries := store.New(pool)
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

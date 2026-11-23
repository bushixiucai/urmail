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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/agent"
	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/api"
	"github.com/bushixiucai/urmail/internal/api/handlers"
	archiveStore "github.com/bushixiucai/urmail/internal/archive"
	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
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
	"github.com/bushixiucai/urmail/internal/maintenance"
	"github.com/bushixiucai/urmail/internal/marketing"
	"github.com/bushixiucai/urmail/internal/notify"
	"github.com/bushixiucai/urmail/internal/reconcile"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
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

	server, cleanup, err := prepareDaemon(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer cleanup()

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

// prepareDaemon validates single-engine database setup, starts approved
// background workers, and constructs the HTTP server without listening.
func prepareDaemon(ctx context.Context, cfg *config.Config, logger *zap.Logger) (*http.Server, func(), error) {
	if cfg == nil {
		return nil, nil, errors.New("configuration is required")
	}
	if cfg.DNSSecondaryOnly {
		return nil, nil, fmt.Errorf("DNS-only secondary configuration is only accepted by urmaild-agent")
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	// Ensure S3 application-layer isolation hooks are active in daemon startup
	// across both SQLite single-engine and PG runtimes.
	db.SetIsolationHook(isolation.DBTXScopeHook())
	db.SetQuerierHook(isolation.WrapQuerier)

	// Validate the encryption authority before opening databases or starting any
	// listener or worker. A missing key must never enable plaintext secret writes.
	secretBox, err := loadRuntimeSecrets(cfg)
	if err != nil {
		return nil, nil, err
	}
	// The webmail render key signs image proxy URLs and link confirmation
	// tokens. An operator may set its own key; otherwise one is derived
	// from the master key, so the feature works out of the box and the
	// key stays stable across restarts. Without a key the privacy layer
	// stays off and remote images remain blocked.
	webmailRenderKey := []byte(cfg.WebmailRenderKey)
	if len(webmailRenderKey) == 0 {
		if deriver, ok := secretBox.(interface{ DeriveKey(string) []byte }); ok {
			webmailRenderKey = deriver.DeriveKey("webmail-render-v1")
		}
	}

	messages, err := i18n.NewStore()
	if err != nil {
		return nil, nil, fmt.Errorf("load message catalog: %w", err)
	}

	baseURL := "https://" + cfg.MyHostname
	if cfg.MyHostname == "" {
		baseURL = "https://box." + cfg.PrimaryDomain
	}
	telemetryStream := cfg.MyHostname
	if telemetryStream == "" {
		telemetryStream = "local"
	}
	smtpAddr := "127.0.0.1:587"
	tokenVerifier := marketingTokenManager(cfg)

	if cfg.IsSQLite() {
		if strings.TrimSpace(cfg.SQLitePath) == "" {
			return nil, nil, errors.New("SQLite mode requires URMAIL_SQLITE_PATH")
		}
		if strings.TrimSpace(cfg.DatabaseURL) != "" ||
			strings.TrimSpace(cfg.DatabaseURLPlatform) != "" ||
			strings.TrimSpace(cfg.DatabaseURLMigrate) != "" ||
			strings.TrimSpace(cfg.DatabaseURLDovecot) != "" {
			return nil, nil, errors.New("single-engine configuration rejects PostgreSQL database URLs in SQLite mode")
		}
		sqliteDB, openErr := sqlitedb.OpenExisting(ctx, cfg.SQLitePath, sqlitedb.ExpectedSchemaVersion)
		if openErr != nil {
			return nil, nil, fmt.Errorf("open verified sqlite database: %w", openErr)
		}
		cleanup := func() { _ = sqliteDB.Close() }

		logger.Info("starting urmaild in SQLite single-engine slice-1 mode; PG pools and unmigrated workers remain fail-closed",
			zap.String("sqlite_path", cfg.SQLitePath),
			zap.Int("schema_version", sqlitedb.ExpectedSchemaVersion),
		)

		webmailVault := webmail.NewMemoryVault()
		go webmailVault.Run(ctx, handlers.SQLiteMailboxStore{DB: sqliteDB}.Get)

		engine := api.New(api.Options{
			RoundcubeEnabled:   cfg.RoundcubeEnabled,
			WebmailNative:      cfg.WebmailNative,
			CardDAVEnabled:     &cfg.CardDAVEnabled,
			CalDAVEnabled:      &cfg.CalDAVEnabled,
			EASEnabled:         &cfg.EASEnabled,
			IMAPAddr:           cfg.IMAPAddr,
			SMTPAddr:           smtpAddr,
			WebmailVault:       webmailVault,
			MetricsToken:       cfg.MetricsToken,
			AICloudEndpoints:   cfg.AICloudEndpoints,
			AILocalEndpoints:   cfg.AILocalEndpoints,
			Quarantined:        cfg.Quarantined,
			WorkersHeld:        cfg.WorkersHeld,
			TelemetryStream:    telemetryStream,
			DevMode:            cfg.DevMode,
			Logger:             logger,
			SQLiteStore:        sqliteDB,
			Messages:           messages,
			Version:            version,
			MyHostname:         cfg.MyHostname,
			Secrets:            secretBox,
			DNSManagedZones:    cfg.DNSManagedZones,
			QuoteHMACKey:       cfg.QuoteHMACKey,
			WebmailRenderKey:   webmailRenderKey,
			BaseURL:            baseURL,
			MarketingTokens:    tokenVerifier,
			MarketingEnabled:   false,
			RoundcubeBridgeKey: cfg.RoundcubeBridgeKey,
		})

		server := &http.Server{
			Addr:              cfg.ListenAddr,
			Handler:           engine,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       60 * time.Second,
			WriteTimeout:      120 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
		return server, cleanup, nil
	}

	if strings.TrimSpace(cfg.SQLitePath) != "" {
		return nil, nil, errors.New("single-engine configuration rejects URMAIL_SQLITE_PATH in PostgreSQL mode")
	}

	var closers []func()
	closeAll := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
	fail := func(err error) (*http.Server, func(), error) {
		closeAll()
		return nil, nil, err
	}

	appPool, err := db.OpenTenantBudgeted(ctx, cfg.DatabaseURL, "core-app", cfg.DatabaseBudgets.Core)
	if err != nil {
		return fail(fmt.Errorf("open tenant database pool: %w", err))
	}
	closers = append(closers, appPool.Close)

	platformPool, err := db.OpenPlatformBudgeted(ctx, cfg.DatabaseURLPlatform, "core-platform", cfg.DatabaseBudgets.Core)
	if err != nil {
		return fail(fmt.Errorf("open platform database pool: %w", err))
	}
	closers = append(closers, platformPool.Close)

	// Seal legacy recovery MIME before any worker can read it, including on a
	// quarantined restore where delivery workers deliberately remain stopped.
	if secretBox != nil {
		if err := mailboxschedule.SealLegacyCopies(ctx, platformPool, secretBox); err != nil {
			return fail(fmt.Errorf("seal legacy webmail recovery material: %w", err))
		}
	}

	marketingPool, err := db.OpenPlatformBudgeted(ctx, cfg.DatabaseURLPlatform, "marketing-platform", cfg.DatabaseBudgets.Marketing)
	if err != nil {
		return fail(fmt.Errorf("open marketing database pool: %w", err))
	}
	closers = append(closers, marketingPool.Close)
	backgroundPool, err := db.OpenPlatformBudgeted(ctx, cfg.DatabaseURLPlatform, "background", cfg.DatabaseBudgets.Background)
	if err != nil {
		return fail(fmt.Errorf("open background database pool: %w", err))
	}
	closers = append(closers, backgroundPool.Close)
	aiAppPool, err := db.OpenTenantBudgeted(ctx, cfg.DatabaseURL, "ai-app", cfg.DatabaseBudgets.AI)
	if err != nil {
		return fail(fmt.Errorf("open AI tenant pool: %w", err))
	}
	closers = append(closers, aiAppPool.Close)
	aiPlatformPool, err := db.OpenPlatformBudgeted(ctx, cfg.DatabaseURLPlatform, "ai-platform", cfg.DatabaseBudgets.AI)
	if err != nil {
		return fail(fmt.Errorf("open AI platform pool: %w", err))
	}
	closers = append(closers, aiPlatformPool.Close)
	marketingAppPool, err := db.OpenTenantBudgeted(ctx, cfg.DatabaseURL, "marketing-app", cfg.DatabaseBudgets.Marketing)
	if err != nil {
		return fail(fmt.Errorf("open marketing tenant pool: %w", err))
	}
	closers = append(closers, marketingAppPool.Close)

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

	// Periodically purge expired sessions so the sessions table does not
	// grow without bound. Expired sessions are already rejected at lookup
	// time; this is pure housekeeping. The app role has no access to
	// sessions (0008), so the platform pool performs the delete.
	go purgeExpiredSessions(ctx, backgroundPool, logger)
	go handlers.RunMFAReaper(ctx)
	startBackgroundWorker(ctx, cfg, func(ctx context.Context) { notify.Run(ctx, backgroundPool, logger) })
	notificationWorker := &notify.Worker{Pool: backgroundPool, Secrets: secretBox, Messages: messages, FallbackLocale: cfg.Locale, BaseURL: baseURL, Enabled: cfg.NotificationsEnabled, DevMode: cfg.DevMode, Quarantined: cfg.Quarantined}
	startBackgroundWorker(ctx, cfg, func(ctx context.Context) { notificationWorker.Run(ctx, logger) })
	reminderWorker := appcollab.ReminderWorker{Pool: backgroundPool, Logger: logger}
	startPeriodicWorker(ctx, cfg, time.Minute, "calendar reminder pass failed", reminderWorker.RunOnce, logger)

	var sieveGateReady func() bool
	if !cfg.DevMode && !cfg.Quarantined {
		sieveGateReady, err = startSieveGate(ctx, platformPool, logger)
		if err != nil {
			return fail(fmt.Errorf("start personal Sieve entitlement gate: %w", err))
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
		// A live upgrade/restore hold must answer inbound and submission
		// attempts with a fixed 4xx so senders retain their queues; the
		// marker is re-read per request and release needs no restart.
		svc.Hold = policy.HoldGate{Path: filepath.Join(maintenance.Root(""), maintenance.DefaultUpgradeHoldFile)}
		svc.InboundChecker = &policy.DBInboundChecker{Store: db.Queries(platformPool)}
		svc.RateLimiter = policy.NewRateLimiter(300, time.Minute)
		svc.QuotaChecker = policy.NewAtomicDBQuotaChecker(
			db.Queries(platformPool), platformPool.Pool, 0,
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
	reconcilePipeline := reconcile.NewPipeline(cfg, &reconcile.VersionedStore{StoreQuerier: db.Queries(platformPool), DB: platformPool.Pool}, cfg.PublicIPv4, cfg.Nameservers).
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
	tokenManager, _ := tokenVerifier.(*domainMail.SuppressionManager)
	marketingWorker, workerErr := marketing.NewWorker(marketing.Options{Pool: marketingPool, Secrets: secretBox, Tokens: tokenManager, Enabled: cfg.MarketingEnabled && cfg.BackgroundWorkersAllowed(), DevMode: cfg.DevMode, Quarantined: cfg.Quarantined,
		Hostname: cfg.MyHostname, UnsubscribeURL: baseURL + "/api/v1/marketing/unsubscribe", Logger: logger})
	if workerErr != nil {
		return fail(fmt.Errorf("configure marketing worker: %w", workerErr))
	}
	startBackgroundWorker(ctx, cfg, marketingWorker.Run)
	flowWorker, flowWorkerErr := marketing.NewFlowWorker(marketing.FlowOptions{
		Pool:    marketingPool,
		Sender:  (&marketing.FlowSender{Pool: marketingPool, Secrets: secretBox, Tokens: tokenManager, Hostname: cfg.MyHostname, UnsubscribeURL: baseURL + "/api/v1/marketing/unsubscribe", Content: marketing.TemplateFlowContent(marketingPool)}).Send,
		Enabled: cfg.MarketingEnabled && cfg.BackgroundWorkersAllowed(), DevMode: cfg.DevMode, Quarantined: cfg.Quarantined, Logger: logger,
	})
	if flowWorkerErr != nil {
		return fail(fmt.Errorf("configure marketing flow worker: %w", flowWorkerErr))
	}
	startBackgroundWorker(ctx, cfg, flowWorker.Run)
	webmailVault := webmail.NewMemoryVault()
	go webmailVault.Run(ctx, handlers.StoreMailboxNative{Q: db.Queries(backgroundPool)}.Get)
	if cfg.WebmailNative && cfg.BackgroundWorkersAllowed() && secretBox != nil {
		schedWorker := mailboxschedule.NewWorker(mailboxschedule.Options{
			Pool:      backgroundPool,
			Secrets:   secretBox,
			Vault:     webmailVault,
			Dialer:    webmail.NetDialer{Addr: cfg.IMAPAddr},
			Submitter: webmail.NetSubmitter{Addr: smtpAddr},
			MaySend:   handlers.MailAllowed(db.Queries(backgroundPool)),
			Logger:    logger,
		})
		go schedWorker.Run(ctx)
	}
	var queueReader handlers.QueueReader
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
		Quarantined:           cfg.Quarantined,
		WorkersHeld:           cfg.WorkersHeld,
		SieveGateReady:        sieveGateReady,
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
		Audit:                 db.Querier(platformPool),
		Secrets:               secretBox,
		DNSProvider:           dnsProvider,
		DNSManagedZones:       cfg.DNSManagedZones,
		DNSPlans:              dnsPlans,
		QuoteHMACKey:          cfg.QuoteHMACKey,
		WebmailRenderKey:      webmailRenderKey,
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
	return server, closeAll, nil
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
	queries := db.Querier(pool)
	systemCtx := isolation.WithScope(ctx, isolation.ScopeContext{Scope: isolation.ScopeSystem})
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := queries.DeleteExpiredSessions(systemCtx); err != nil {
				logger.Warn("purge expired sessions", zap.Error(err))
			}
		}
	}
}

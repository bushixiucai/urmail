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

	// Periodically purge expired sessions so the sessions table does not
	// grow without bound. Expired sessions are already rejected at lookup
	// time; this is pure housekeeping. The app role has no access to
	// sessions (0008), so the platform pool performs the delete.
	go purgeExpiredSessions(ctx, platformPool, logger)
	go handlers.RunMFAReaper(ctx)
	if !cfg.Quarantined {
		go payment.RunWebhookMaintenance(ctx, platformPool, logger)
	}
	go notify.Run(ctx, platformPool, logger)
	notificationWorker := &notify.Worker{Pool: platformPool, Secrets: secretBox, Messages: messages, FallbackLocale: cfg.Locale, BaseURL: baseURL, Enabled: cfg.NotificationsEnabled, DevMode: cfg.DevMode, Quarantined: cfg.Quarantined}
	go notificationWorker.Run(ctx, logger)
	renewals := billingrenew.Worker{Store: store.New(platformPool.Pool), Settlements: billingrenew.PoolSettlementStore{Pool: platformPool}}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			if err := renewals.RunOnce(ctx); err != nil && ctx.Err() == nil {
				logger.Warn("renewal attempt failed", zap.Error(err))
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	// OPS-001: automated encrypted offsite backups. Disabled unless the
	// operator configures both the age recipient and an offsite destination;
	// the privileged local backup runs through the agent's fixed
	// backup.local.create operation (no caller-controlled parameters).
	if backupRecipient := os.Getenv("URMAIL_BACKUP_AGE_RECIPIENT"); backupRecipient != "" {
		if backupURI := os.Getenv("URMAIL_BACKUP_OFFSITE_URI"); backupURI != "" {
			backupWorker, err := newBackupWorker(cfg, platformPool, backupRecipient, backupURI)
			if err != nil {
				logger.Warn("automated offsite backup disabled", zap.Error(err))
			} else {
				go backupWorker.Run(ctx, logger)
			}
		}
	}

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
		spamMilter := &mailspam.Server{Addr: mailspam.DefaultAddr, Handler: spamVerdict}
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
	reconcilePipeline := reconcile.NewPipeline(cfg, &reconcile.VersionedStore{StoreQuerier: store.New(platformPool.Pool), DB: platformPool.Pool}, cfg.PublicIPv4, cfg.Nameservers).WithLogger(logger)
	if !cfg.Quarantined {
		go reconcilePipeline.StartWorker(ctx, 2*time.Second, func(files map[string]string) error {
			if cfg.DevMode {
				logger.Info("development configuration rendered without privileged apply", zap.Int("files", len(files)))
				return nil
			}
			snapshot := reconcilePipeline.CurrentSnapshot()
			if snapshot == nil {
				return fmt.Errorf("configuration snapshot unavailable")
			}
			_, err := agent.NewClient(cfg.AgentSocket).Do(ctx, agentd.OpConfigApply, agentd.ApplyParams{Postfix: snapshot.PostfixSnap, DNS: snapshot.NSDSnap, DKIM: snapshot.DKIM, SpamPolicies: snapshot.DovecotNode.SpamPolicies})
			return err
		})

		// Render once on startup so a fresh install does not keep the stock
		// distribution configuration until the first tenant write.
		reconcilePipeline.Trigger()
	}

	if !cfg.DevMode && !cfg.Quarantined {
		archiveWorker := &archiveStore.Worker{Pool: platformPool}
		go archiveWorker.Start(ctx, logger)
	}
	dnsPlans := startDomainOnboarding(ctx, cfg, platformPool, secretBox, reconcilePipeline, logger)
	startMailTelemetry(ctx, cfg, platformPool, logger)
	startMailObservability(ctx, cfg, platformPool, logger)
	startPaymentRecovery(ctx, cfg, platformPool, secretBox, baseURL, stripe, logger)
	refunds := &refundService.Service{Pool: platformPool, Registry: &payment.Registry{Store: store.New(platformPool), Secrets: secretBox, BaseURL: baseURL}, LegacyStripe: stripe, Enabled: cfg.RefundsEnabled, DevMode: cfg.DevMode, Quarantined: cfg.Quarantined}
	go refunds.Run(ctx, logger)
	tokenVerifier := marketingTokenManager(cfg)
	tokenManager, _ := tokenVerifier.(*domainMail.SuppressionManager)
	marketingWorker, workerErr := marketing.NewWorker(marketing.Options{Pool: platformPool, Secrets: secretBox, Tokens: tokenManager, Enabled: cfg.MarketingEnabled, DevMode: cfg.DevMode, Quarantined: cfg.Quarantined,
		Hostname: cfg.MyHostname, UnsubscribeURL: baseURL + "/api/v1/marketing/unsubscribe", Logger: logger})
	if workerErr != nil {
		return fmt.Errorf("configure marketing worker: %w", workerErr)
	}
	go marketingWorker.Run(ctx)
	flowWorker, flowWorkerErr := marketing.NewFlowWorker(marketing.FlowOptions{
		Pool:    platformPool,
		Sender:  (&marketing.FlowSender{Pool: platformPool, Secrets: secretBox, Tokens: tokenManager, Hostname: cfg.MyHostname, UnsubscribeURL: baseURL + "/api/v1/marketing/unsubscribe", Content: marketing.TemplateFlowContent(platformPool)}).Send,
		Enabled: cfg.MarketingEnabled, DevMode: cfg.DevMode, Quarantined: cfg.Quarantined, Logger: logger,
	})
	if flowWorkerErr != nil {
		return fmt.Errorf("configure marketing flow worker: %w", flowWorkerErr)
	}
	go flowWorker.Run(ctx)
	webmailVault := webmail.NewMemoryVault()
	go webmailVault.Run(ctx, handlers.StoreMailboxNative{Q: store.New(platformPool.Pool)}.Get)
	smtpAddr := "127.0.0.1:587"
	if cfg.WebmailNative && !cfg.Quarantined && secretBox != nil {
		schedWorker := mailboxschedule.NewWorker(mailboxschedule.Options{
			Pool:      platformPool,
			Secrets:   secretBox,
			Vault:     webmailVault,
			Dialer:    webmail.NetDialer{Addr: cfg.IMAPAddr},
			Submitter: webmail.NetSubmitter{Addr: smtpAddr},
			MaySend:   handlers.MailAllowed(store.New(platformPool.Pool)),
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
		RoundcubeEnabled:    cfg.RoundcubeEnabled,
		WebmailNative:       cfg.WebmailNative,
		CardDAVEnabled:      &cfg.CardDAVEnabled,
		CalDAVEnabled:       &cfg.CalDAVEnabled,
		EASEnabled:          &cfg.EASEnabled,
		IMAPAddr:            cfg.IMAPAddr,
		SMTPAddr:            smtpAddr,
		WebmailVault:        webmailVault,
		MetricsToken:        cfg.MetricsToken,
		SieveActivator:      sieveActivator,
		AICloudEndpoints:    cfg.AICloudEndpoints,
		AILocalEndpoints:    cfg.AILocalEndpoints,
		RegistrationEnabled: cfg.RegistrationEnabled,
		Quarantined:         cfg.Quarantined,
		Recall:              recallClient,
		Queue:               queueReader,
		TelemetryStream:     telemetryStream,
		DevMode:             cfg.DevMode,
		Logger:              logger,
		AppPool:             appPool,
		PlatformPool:        platformPool,
		Messages:            messages,
		Version:             version,
		MyHostname:          cfg.MyHostname,
		Reconcile:           reconcilePipeline,
		Audit:               store.New(platformPool.Pool),
		Secrets:             secretBox,
		DNSProvider:         dnsProvider,
		DNSManagedZones:     cfg.DNSManagedZones,
		DNSPlans:            dnsPlans,
		Payment:             stripe,
		QuoteHMACKey:        cfg.QuoteHMACKey,
		BaseURL:             baseURL,
		MarketingTokens:     tokenVerifier,
		RoundcubeBridgeKey:  cfg.RoundcubeBridgeKey,
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

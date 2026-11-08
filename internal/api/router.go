// Package api wires the HTTP surface of urMail.cn.
package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/handlers"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/autoconfig"
	"github.com/bushixiucai/urmail/internal/caldav"
	"github.com/bushixiucai/urmail/internal/carddav"
	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/dnsprovider"
	"github.com/bushixiucai/urmail/internal/eas"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/onboarding"
	"github.com/bushixiucai/urmail/internal/rendercache"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/web"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// Options carries the dependencies of the HTTP server.
type Options struct {
	DNSAuthorityAgent  handlers.DNSAuthorityAgent
	BrandHostTXTLookup func(context.Context, string) ([]string, error)
	// ArchiveRoot overrides the trusted archive storage root for isolated hosts.
	ArchiveRoot      string
	DevMode          bool
	Quarantined      bool
	MarketingEnabled bool
	WorkersHeld      bool
	SieveGateReady   func() bool
	DNSPlans         *onboarding.Service
	Logger           *zap.Logger
	// AppPool is the tenant-scoped pool.
	AppPool *db.Pool
	// PlatformPool is the platform-scoped pool.
	PlatformPool *db.Pool
	// Dedicated workload pools; nil preserves legacy embedders and tests.
	AIAppPool             *db.Pool
	AIPlatformPool        *db.Pool
	MarketingAppPool      *db.Pool
	MarketingPlatformPool *db.Pool
	// Messages is the message catalog.
	Messages *i18n.Store
	// Version is reported by /api/v1/healthz.
	Version string
	// MyHostname is the public MX host advertised in domain DNS guidance.
	MyHostname string
	// Reconcile is the configuration reconcile trigger.
	Reconcile handlers.ReconcileTrigger
	// Recall processes persisted local mailbox recalls through the root agent.
	Recall             handlers.RecallExpunger
	Queue              handlers.QueueReader
	TelemetryStream    string
	RoundcubeBridgeKey string
	RoundcubeEnabled   bool
	WebmailNative      bool
	// Nil preserves the historical protocol-enabled behavior for embedders.
	CardDAVEnabled *bool
	CalDAVEnabled  *bool
	EASEnabled     *bool
	IMAPAddr       string
	// SMTPAddr is the loopback submission endpoint for native webmail send.
	// Empty defaults to 127.0.0.1:587.
	SMTPAddr         string
	AICloudEndpoints []string
	AILocalEndpoints []string
	SieveActivator   handlers.SieveActivator
	MetricsToken     string
	// WebmailVault holds IMAP passwords for live native sessions (process memory).
	WebmailVault webmail.CredentialVault
	// WebmailSubmitter is the loopback submission endpoint for mailbox-originated
	// outbound mail such as calendar invitations.
	WebmailSubmitter webmail.Submitter

	// Audit receives one row per successful mutating tenant request.
	Audit middleware.AuditWriter
	// Secrets encrypts TOTP seeds at rest. Nil keeps legacy plaintext support.
	Secrets handlers.SecretsBox
	// DNSProvider writes verification records for delegated domains when set.
	DNSProvider dnsprovider.Provider
	// DNSManagedZones allow-lists the zones DNSProvider may write; a domain
	// outside them verifies manually (M5 review 3.11).
	DNSManagedZones []string
	// QuoteHMACKey signs support diagnostics.
	QuoteHMACKey string
	// WebmailRenderKey signs webmail image proxy URLs and link
	// confirmation tokens. An empty key disables both: remote images stay
	// blocked and links keep their plain form.
	WebmailRenderKey []byte
	// BaseURL is the public panel origin used for Stripe redirects.
	BaseURL string
	// MarketingTokens verifies stateless, signed unsubscribe tokens.
	MarketingTokens interface {
		VerifyUnsubscribeToken(string) (int64, string, bool)
	}
}

// workloadPoolDefaults keeps existing embedders working while production supplies
// explicit capacity-isolated pools.
func workloadPoolDefaults(opts Options) Options {
	if opts.AIAppPool == nil {
		opts.AIAppPool = opts.AppPool
	}
	if opts.AIPlatformPool == nil {
		opts.AIPlatformPool = opts.PlatformPool
	}
	if opts.MarketingAppPool == nil {
		opts.MarketingAppPool = opts.AppPool
	}
	if opts.MarketingPlatformPool == nil {
		opts.MarketingPlatformPool = opts.PlatformPool
	}
	return opts
}

func workloadTenantGroup(v1 *gin.RouterGroup, opts Options, auth *store.Queries, pool *db.Pool) *gin.RouterGroup {
	group := v1.Group("")
	if opts.Audit != nil {
		group.Use(middleware.Audit(opts.Audit, opts.Logger))
	}
	group.Use(middleware.RequireAuth(auth), middleware.RequireTenantTx(pool, opts.Logger), middleware.RequireCSRF())
	return group
}

// New builds the gin engine with all middleware and routes attached.
func New(opts Options) *gin.Engine {
	opts = workloadPoolDefaults(opts)
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	gin.SetMode(gin.ReleaseMode)

	engine := gin.New()
	// Dev stacks listen on 127.0.0.1:<dev port>, which can never match the
	// configured canonical mail host; pinning it there would 403 every
	// platform-admin request. Production keeps the canonical pin.
	devCanonical := opts.MyHostname
	if opts.DevMode {
		devCanonical = ""
	}
	engine.Use(middleware.BrandHostBoundary(opts.PlatformPool, devCanonical))
	engine.Use(collaborationProtocolGate(opts))
	clientConfig := autoconfig.Config{PrimaryMailHost: opts.MyHostname, POP3Enabled: true, CardDAVPath: "/api/v1/carddav/", CalDAVPath: "/caldav/", EASPath: "/Microsoft-Server-ActiveSync"}
	if !protocolEnabled(opts.CardDAVEnabled) {
		clientConfig.CardDAVPath = "-"
	}
	if !protocolEnabled(opts.CalDAVEnabled) {
		clientConfig.CalDAVPath = "-"
	}
	if !protocolEnabled(opts.EASEnabled) {
		clientConfig.EASPath = "-"
	}
	autoconfig.Mount(engine, clientConfig)

	// The reverse proxy is the only trusted peer. Client-supplied
	// X-Forwarded-For headers must not influence rate limiting or audit IPs.
	_ = engine.SetTrustedProxies([]string{"127.0.0.1", "::1"})
	engine.Use(middleware.RequestID())
	engine.Use(middleware.HTTPMetrics())
	engine.Use(middleware.Recover(opts.Logger))
	engine.Use(middleware.AccessLog(opts.Logger))
	if opts.Quarantined {
		engine.Use(func(c *gin.Context) {
			readOnly := c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead || c.Request.Method == http.MethodOptions
			authException := false
			if c.Request.Method == http.MethodPost {
				switch c.Request.URL.Path {
				case "/api/v1/auth/login", "/api/v1/auth/mfa/verify", "/api/v1/auth/logout", "/api/v1/auth/switch-tenant":
					authException = true
				}
			}
			if readOnly || authException {
				c.Next()
				return
			}
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "RESTORE_QUARANTINED", "message": "error.RESTORE_QUARANTINED"}})
		})
	}

	engine.Use(middleware.BodyLimitWithMailboxUploads(1<<20, 16<<20))
	engine.Use(middleware.Timeout(30 * time.Second))

	// /metrics is operator-facing telemetry and must not be world-readable
	// (M6 review D12). With an auth store it requires a platform admin
	// session; the fallback keeps bare builds (no auth store) working and is
	// only reachable on deployments that never configure one.
	// NOTE: the platform admin group uses the same middleware set.

	// Bundled web assets
	engine.GET("/assets/*filepath", gin.WrapH(web.AssetHandler()))

	// Authentication runs against the platform pool: users, sessions and
	// tenants are covered by RLS, and the platform role bypasses it. All
	// tenant-scoped handlers run inside the RequireTenantTx transaction
	// instead, where the RLS context is published with SET LOCAL.
	var authQueries *store.Queries
	if opts.PlatformPool != nil {
		authQueries = store.New(opts.PlatformPool)
	}
	var appQueries *store.Queries
	if opts.AppPool != nil {
		appQueries = store.New(opts.AppPool)
	}

	var aiPlatformQueries, marketingAppQueries, marketingPlatformQueries *store.Queries
	if opts.AIPlatformPool != nil {
		aiPlatformQueries = store.New(opts.AIPlatformPool)
	}
	if opts.MarketingAppPool != nil {
		marketingAppQueries = store.New(opts.MarketingAppPool)
	}
	if opts.MarketingPlatformPool != nil {
		marketingPlatformQueries = store.New(opts.MarketingPlatformPool)
	}

	authHandler := handlers.NewAuthHandler(authQueries)
	authHandler.Secrets = opts.Secrets
	var platformQueries *store.Queries
	if opts.PlatformPool != nil {
		platformQueries = store.New(opts.PlatformPool)
	}
	tenantHandler := handlers.NewTenantHandler(appQueries, platformQueries)
	onboardingHandler := &handlers.OnboardingHandler{Store: appQueries}
	dnsPlanHandler := &handlers.DNSPlanHandler{Store: appQueries, Service: opts.DNSPlans}
	domainHandler := handlers.NewDomainHandler(appQueries, opts.Reconcile)
	domainHandler.DNS = opts.DNSProvider
	domainHandler.DNSManagedZones = opts.DNSManagedZones
	domainHandler.Hostname = opts.MyHostname
	domainHandler.Verification = handlers.NewDomainVerificationRepository(opts.AppPool)
	mailboxHandler := handlers.NewMailboxHandler(appQueries, opts.Reconcile)
	aliasHandler := handlers.NewAliasHandler(appQueries, opts.Reconcile)
	archiveHandler := handlers.NewArchiveHandler(appQueries)
	archiveEvidence := handlers.ArchiveEvidence{Q: platformQueries}
	archiveContent := &handlers.ArchiveContentHandler{Store: appQueries, Root: opts.ArchiveRoot}
	transportHandler := handlers.NewTransportSecurityHandler(opts.PlatformPool, opts.AppPool, opts.MyHostname, opts.Reconcile)
	engine.GET("/.well-known/mta-sts.txt", transportHandler.Policy)
	engine.POST("/api/v1/tls-reports/:fqdn", middleware.RateLimitWindow(30, time.Minute, func(c *gin.Context) (string, bool) { return c.ClientIP() + "/" + c.Param("fqdn"), true }), transportHandler.Intake)
	supportHandler := &handlers.SupportHandler{Store: appQueries, Platform: platformQueries, PlatformPool: opts.PlatformPool, DiagnosticKey: []byte(opts.QuoteHMACKey)}
	recallHandler := &handlers.RecallHandler{AppPool: opts.AppPool, Store: appQueries, Platform: platformQueries, Expunger: opts.Recall}
	auditHandler := handlers.NewAuditHandler(appQueries)
	complianceGrantHandler := &handlers.ComplianceGrantHandler{Store: appQueries}
	brandingHandler := handlers.NewBrandingHandler(platformQueries)
	brandingHandler.PrimaryHost = opts.MyHostname
	brandingHandler.Reconcile = opts.Reconcile
	platformHandler := handlers.NewPlatformHandler(platformQueries)
	platformHandler.Pool = opts.PlatformPool
	platformHandler.Reconcile = opts.Reconcile
	legacyTenantID := int64(1)
	if platformQueries != nil {
		if tenants, err := platformQueries.PlatformListTenants(context.Background()); err == nil && len(tenants) > 0 {
			legacyTenantID = tenants[0].ID
		}
	}
	legacyHandler := handlers.NewLegacyHandler(authQueries, platformQueries, legacyTenantID, opts.Version)
	notificationHandler := &handlers.NotificationHandler{Store: appQueries}
	platformNotifications := &handlers.NotificationHandler{Store: platformQueries}
	bridgeKey := []byte(opts.RoundcubeBridgeKey)
	if !opts.RoundcubeEnabled {
		bridgeKey = nil
	}
	sieveBridge := &handlers.SieveBridgeHandler{Pool: opts.PlatformPool, Key: bridgeKey, Activator: opts.SieveActivator}
	mailboxAI := &handlers.MailboxAIService{Pool: opts.AIPlatformPool, Secrets: opts.Secrets, CloudEndpoints: opts.AICloudEndpoints, LocalEndpoints: opts.AILocalEndpoints}
	aiConfiguration := &handlers.AIConfigurationHandler{HealthRepository: handlers.NewAIHealthRepository(opts.AIPlatformPool), Pool: opts.AIPlatformPool, Secrets: opts.Secrets, CloudEndpoints: opts.AICloudEndpoints, LocalEndpoints: opts.AILocalEndpoints}
	mailboxBridge := &handlers.MailboxBridgeHandler{Pool: opts.PlatformPool, Key: bridgeKey, AI: mailboxAI, Sieve: opts.SieveActivator}
	webmailVault := opts.WebmailVault
	if webmailVault == nil {
		webmailVault = webmail.NewMemoryVault()
	}
	if revoker, ok := webmailVault.(interface{ DeleteMailbox(int64, int64) }); ok {
		mailboxHandler.Vault = revoker
	}
	nativeStore := handlers.StoreMailboxNative{Q: platformQueries}
	smtpAddr := opts.SMTPAddr
	if smtpAddr == "" {
		smtpAddr = "127.0.0.1:587"
	}
	aiDrafts := handlers.AIDrafts{Q: aiPlatformQueries}
	aiSearchAsk := &handlers.AISearchAsk{Pool: opts.AIPlatformPool, Dialer: webmail.NetDialer{Addr: opts.IMAPAddr}, Vault: webmailVault, ModelFor: handlers.TenantCitationModel(opts.AIPlatformPool, opts.Secrets, opts.AICloudEndpoints, opts.AILocalEndpoints)}
	aiSearchAsk.Accounting = handlers.AISearchAskAccounting{Pool: opts.AIPlatformPool}
	aiBudgetStatus := &handlers.AISearchAskAccounting{Pool: opts.AIPlatformPool}
	mailboxNative := &handlers.MailboxNativeHandler{
		MailHost:         opts.MyHostname,
		Enabled:          opts.WebmailNative && platformQueries != nil,
		Auth:             nativeStore,
		Sessions:         nativeStore,
		Vault:            webmailVault,
		Dialer:           webmail.NetDialer{Addr: opts.IMAPAddr, AuthenticationSecrets: opts.Secrets},
		Submitter:        webmail.NetSubmitter{Addr: smtpAddr},
		Secrets:          opts.Secrets,
		AILocalEndpoints: opts.AILocalEndpoints,
		Pool:             opts.PlatformPool,
		MaySend:          handlers.MailAllowed(platformQueries),
		RenderKey:        opts.WebmailRenderKey,
		RenderCache:      rendercache.New(),
		// WEBMAIL-005/006 mailbox self-service: signature, identities,
		// quota and managed-sieve rules through the existing safe pipeline.
		SelfSettings: handlers.StoreMailboxSelfSettings{Pool: opts.PlatformPool},
		Rules:        handlers.StoreMailboxRules{Pool: opts.PlatformPool},
		Sieve:        opts.SieveActivator,
	}
	if opts.WebmailSubmitter != nil {
		mailboxNative.Submitter = opts.WebmailSubmitter
	}
	if protocolEnabled(opts.EASEnabled) && platformQueries != nil {
		easServer := eas.NewServer(opts.PlatformPool)
		easServer.Vault = webmailVault
		easServer.Submitter = mailboxNative.Submitter
		easServer.Dialer = mailboxNative.Dialer
		easServer.MaySend = mailboxNative.MaySend
		easServer.SenderAllowed = mailboxNative.SelfSettings.SenderIdentityAllowed
		easServer.EmailFolders = eas.EmailFoldersFrom(mailboxNative.Dialer, webmailVault)
		easServer.Mail = eas.MailSourceFromDialer(mailboxNative.Dialer, webmailVault)
		mailboxNative.EAS = easServer
	}
	// AI methods get their own pool without moving core mailbox operations.
	mailboxNativeAI := *mailboxNative
	mailboxNativeAI.Pool = opts.AIPlatformPool
	contactsHandler := handlers.MailboxContactsHandler{Service: appcollab.Service{Q: platformQueries}, GroupStore: platformQueries, Groups: platformQueries}
	calendarHandler := handlers.MailboxCalendarHandler{Native: mailboxNative, Pool: opts.PlatformPool, Service: appcollab.Service{Q: platformQueries}, Submitter: mailboxNative.Submitter, Vault: webmailVault, MaySend: mailboxNative.MaySend}
	deliveryHandler := &handlers.DeliveryHandler{Store: appQueries, Platform: platformQueries, Queue: opts.Queue, Stream: opts.TelemetryStream}
	marketingHandler := &handlers.MarketingHandler{Store: marketingAppQueries, Platform: marketingPlatformQueries, Secrets: opts.Secrets}
	marketingSuppressionHandler := &handlers.MarketingSuppressionHandler{Store: marketingAppQueries, Tokens: opts.MarketingTokens, AppPool: opts.MarketingAppPool, Messages: opts.Messages}

	engine.GET("/internal/metrics", middleware.MetricsAccess(opts.MetricsToken), gin.WrapH(promhttp.Handler()))
	// DAV-001 CardDAV adapter. It authenticates mailbox Bearer sessions
	// itself (same lookup as RequireMailboxAuth) and needs the WebDAV methods
	// PROPFIND/REPORT, which gin's Any() does not cover.
	protocolAuth := mailboxNative.ProtocolAuth()
	if platformQueries != nil && protocolEnabled(opts.CardDAVEnabled) {
		carddavServer := &carddav.Handler{Service: appcollab.Service{Q: platformQueries}, Sessions: nativeStore, Prefix: "/api/v1/carddav"}
		engine.Any("/.well-known/carddav", gin.WrapH(carddavServer))
		v1Any := func(method, path string) {
			engine.Handle(method, path, protocolAuth, gin.WrapH(carddavServer))
		}
		for _, m := range []string{"GET", "HEAD", "PUT", "DELETE", "OPTIONS", "PROPFIND", "REPORT"} {
			v1Any(m, "/api/v1/carddav/*path")
		}
	}
	// Mount the writable CalDAV server, not the legacy read-only export
	// helper. Both aliases keep one authenticated mailbox principal.
	for _, method := range []string{"GET", "HEAD", "OPTIONS", "PROPFIND", "REPORT"} {
		engine.Handle(method, "/.well-known/caldav", func(c *gin.Context) {
			c.Redirect(http.StatusMovedPermanently, "/caldav/")
		})
	}
	if platformQueries != nil && protocolEnabled(opts.CalDAVEnabled) {
		calendarServer := caldav.NewServer(platformQueries)
		serveCalendar := func(c *gin.Context) {
			session, ok := middleware.GetMailboxSession(c)
			if !ok {
				c.AbortWithStatus(http.StatusUnauthorized)
				return
			}
			ctx := caldav.WithSession(c.Request.Context(), caldav.Session{TenantID: session.TenantID, MailboxID: session.MailboxID})
			request := c.Request.WithContext(ctx)
			if strings.HasPrefix(request.URL.Path, "/api/v1/caldav") {
				urlCopy := *request.URL
				urlCopy.Path = "/caldav" + strings.TrimPrefix(urlCopy.Path, "/api/v1/caldav")
				request.URL = &urlCopy
			}
			calendarServer.ServeHTTP(c.Writer, request)
		}
		for _, method := range []string{"GET", "HEAD", "PUT", "DELETE", "OPTIONS", "PROPFIND", "REPORT"} {
			engine.Handle(method, "/caldav/*path", protocolAuth, serveCalendar)
			engine.Handle(method, "/api/v1/caldav/*path", protocolAuth, serveCalendar)
		}
	}
	// Standard devices authenticate here, before the shared EAS transport.
	engine.OPTIONS("/Microsoft-Server-ActiveSync", protocolAuth, mailboxNative.EASOptions)
	engine.POST("/Microsoft-Server-ActiveSync", protocolAuth, mailboxNative.EASCommand)
	engine.OPTIONS("/api/v1/eas", protocolAuth, mailboxNative.EASOptions)
	engine.POST("/api/v1/eas", protocolAuth, mailboxNative.EASCommand)
	v1 := engine.Group("/api/v1")
	{
		v1.GET("/healthz", healthz(opts))
		v1.GET("/roundcube/status", func(c *gin.Context) { c.JSON(200, gin.H{"enabled": opts.RoundcubeEnabled}) })
		if opts.RoundcubeEnabled {
			v1.POST("/roundcube/bridge", mailboxBridge.Bridge)
		}
		v1.GET("/mailbox/status", mailboxNative.Status)
		// The image proxy is authorized by the signed URL alone: an image
		// inside the sandboxed message frame cannot present the mailbox
		// bearer token. A bearer that is sent anyway must belong to the
		// signed mailbox.
		v1.GET("/mailbox/images/proxy", middleware.RateLimitWindow(600, time.Minute, func(c *gin.Context) (string, bool) {
			return c.ClientIP(), true
		}), mailboxNative.ImageProxy)
		v1.GET("/mailbox/links/confirm", mailboxNative.LinkConfirm)
		v1.POST("/mailbox/links/confirm", middleware.RateLimitWindow(60, time.Minute, func(c *gin.Context) (string, bool) {
			return c.ClientIP(), true
		}), mailboxNative.LinkConfirm)
		v1.GET("/internal/status/ready", func(c *gin.Context) {
			c.Set("statusReadyQuerier", opts.PlatformPool)
			handlers.StatusReady(c)
		})
		v1.POST("/mailbox/session", middleware.RateLimitWindow(20, time.Minute, func(c *gin.Context) (string, bool) {
			return c.ClientIP(), true
		}), mailboxNative.CreateSession)
		mailboxAuthed := v1.Group("/mailbox")
		mailboxAuthed.Use(middleware.RequireMailboxAuth(nativeStore, webmailVault))
		{
			mailboxAuthed.GET("/folders", mailboxNative.ListFolders)
			mailboxAuthed.POST("/folders", mailboxNative.CreateFolder)
			mailboxAuthed.PATCH("/folders/:name", mailboxNative.RenameFolder)
			mailboxAuthed.DELETE("/folders/:name", mailboxNative.DeleteFolder)
			mailboxAuthed.POST("/folders/:name/messages/copy", mailboxNative.CopyMessages)
			mailboxAuthed.POST("/folders/:name/messages/trash", mailboxNative.TrashMessages)
			mailboxAuthed.POST("/folders/:name/messages/restore", mailboxNative.RestoreMessages)
			// WEBMAIL-004 cross-folder search (bounded fan-out + date merge).
			mailboxAuthed.GET("/search", mailboxNative.CrossFolderSearch)
			mailboxAuthed.GET("/folders/:name/messages", mailboxNative.ListMessages)
			mailboxAuthed.POST("/folders/:name/messages/flags", mailboxNative.FlagMessages)
			mailboxAuthed.POST("/folders/:name/messages/move", mailboxNative.MoveMessages)
			mailboxAuthed.POST("/folders/:name/messages/delete", mailboxNative.DeleteMessages)
			mailboxAuthed.GET("/folders/:name/messages/:uid", mailboxNative.GetMessage)
			mailboxAuthed.GET("/folders/:name/messages/:uid/attachments/:part_id", mailboxNative.GetAttachment)
			mailboxAuthed.GET("/folders/:name/messages/:uid/raw", mailboxNative.GetRawMessage)
			handlers.RegisterMailboxImportRoutes(mailboxAuthed, mailboxNative)
			mailboxAuthed.POST("/folders/:name/import", mailboxNative.ImportMessage)
			mailboxAuthed.POST("/send", mailboxNative.SendMessage)
			mailboxAuthed.GET("/send-receipts", mailboxNative.ListSendReceipts)
			mailboxAuthed.GET("/send-receipts/:id", mailboxNative.GetSendReceipt)
			mailboxAuthed.POST("/send-receipts/:id/repair-sent-copy", mailboxNative.RepairSendReceiptCopy)
			mailboxAuthed.POST("/scheduled-sends/:id/acknowledge-uncertain", mailboxNative.AcknowledgeUncertainScheduledSend)
			mailboxAuthed.PUT("/drafts", mailboxNative.SaveDraft)
			mailboxAuthed.GET("/drafts", mailboxNative.ListDrafts)
			mailboxAuthed.DELETE("/drafts/:uid", mailboxNative.DeleteDraft)
			mailboxAuthed.POST("/scheduled-sends", mailboxNative.CreateScheduledSend)
			mailboxAuthed.GET("/scheduled-sends", mailboxNative.ListScheduledSends)
			mailboxAuthed.DELETE("/scheduled-sends/:id", mailboxNative.CancelScheduledSend)
			mailboxAuthed.POST("/scheduled-sends/:id/repair-sent-copy", mailboxNative.RepairScheduledSentCopy)
			mailboxAuthed.POST("/password", mailboxNative.ChangePassword)
			// WEBMAIL-005 signature, display name, preferences, identities.
			mailboxAuthed.GET("/settings", mailboxNative.GetSettings)
			mailboxAuthed.PUT("/settings", mailboxNative.PutSettings)
			mailboxAuthed.GET("/identities", mailboxNative.ListIdentities)
			// WEBMAIL-006 server-authoritative quota visibility.
			mailboxAuthed.GET("/quota", mailboxNative.GetQuota)
			// WEBMAIL-006 user self-service managed-sieve rules.
			mailboxAuthed.GET("/rules", mailboxNative.GetRules)
			mailboxAuthed.PUT("/rules", mailboxNative.PutRules)
			// MAIL-002: user spam feedback drives the per-mailbox allow/deny
			// delivery policies; private rules never classify another recipient.
			spamFeedbackHandler := &handlers.MailboxSpamFeedback{Pool: opts.AppPool, Reconcile: opts.Reconcile}
			mailboxAuthed.POST("/spam/feedback", spamFeedbackHandler.Feedback)
			handlers.RegisterMailboxSpamSenderRoutes(mailboxAuthed, spamFeedbackHandler)
			handlers.RegisterMailboxSnippetRoutes(mailboxAuthed, &handlers.MailboxSnippetsHandler{Pool: opts.AppPool})
			handlers.RegisterMailboxCorporateDirectoryRoutes(mailboxAuthed, &handlers.MailboxCorporateDirectoryHandler{Pool: opts.AppPool})
			mailboxAuthed.GET("/contacts", contactsHandler.ListContacts)
			mailboxDataImport := handlers.MailboxDataImport{Pool: opts.PlatformPool}
			mailboxAuthed.POST("/data/import/:action", mailboxDataImport.Handle)
			mailboxAuthed.GET("/data/imports", mailboxDataImport.History)
			mailboxAuthed.POST("/contacts", contactsHandler.CreateContact)
			mailboxAuthed.PUT("/contacts/:id", contactsHandler.UpdateContact)
			mailboxAuthed.DELETE("/contacts/:id", contactsHandler.DeleteContact)
			mailboxAuthed.GET("/contacts/:id/vcard", contactsHandler.ExportContact)
			mailboxAuthed.GET("/contacts/export.vcf", contactsHandler.ExportContactsVCF)
			mailboxAuthed.POST("/contacts/import", contactsHandler.ImportContact)
			mailboxAuthed.POST("/contacts/import.csv", contactsHandler.ImportContactsCSV)
			mailboxAuthed.GET("/contacts/groups", contactsHandler.ListGroups)
			mailboxAuthed.POST("/contacts/groups", contactsHandler.CreateGroup)
			mailboxAuthed.DELETE("/contacts/groups/:id", contactsHandler.DeleteGroup)
			mailboxAuthed.POST("/contacts/groups/members", contactsHandler.AddGroupMember)
			mailboxAuthed.GET("/contacts/groups/:id/members", contactsHandler.ListGroupMembers)
			mailboxAuthed.DELETE("/contacts/groups/:id/members/:contact", contactsHandler.RemoveGroupMember)
			mailboxAuthed.GET("/carddav/addressbook", contactsHandler.CardDAVReport)
			mailboxAuthed.Handle("PROPFIND", "/carddav/addressbook", contactsHandler.CardDAVPropfind)
			mailboxAuthed.PUT("/carddav/addressbook", contactsHandler.CardDAVWrite)
			mailboxAuthed.DELETE("/carddav/addressbook", contactsHandler.CardDAVWrite)
			mailboxAuthed.GET("/caldav/calendar", calendarHandler.CalDAVReport)
			mailboxAuthed.Handle("PROPFIND", "/caldav/calendar", calendarHandler.CalDAVPropfind)
			mailboxAuthed.PUT("/caldav/calendar", calendarHandler.CalDAVWrite)
			mailboxAuthed.DELETE("/caldav/calendar", calendarHandler.CalDAVWrite)
			mailboxAuthed.OPTIONS("/eas", mailboxNative.EASOptions)
			mailboxAuthed.POST("/eas", mailboxNative.EASCommand)
			mailboxAuthed.POST("/ai/drafts", aiDrafts.Create)
			mailboxAuthed.GET("/ai/drafts/:id", aiDrafts.Get)
			mailboxAuthed.PUT("/ai/drafts/:id", aiDrafts.Update)
			mailboxAuthed.POST("/ai/search-ask", aiSearchAsk.Handle)
			mailboxAuthed.GET("/ai/search-ask/status", aiSearchAsk.Status)
			mailboxAuthed.POST("/ai/search-ask/clear", aiSearchAsk.Clear)
			mailboxAuthed.DELETE("/ai/search-ask/index", aiSearchAsk.Clear)
			mailboxAuthed.GET("/ai/index/status", aiSearchAsk.Status)
			mailboxAuthed.POST("/ai/index/clear", aiSearchAsk.Clear)
			mailboxAuthed.DELETE("/ai/index", aiSearchAsk.Clear)
			handlers.RegisterNativeMailboxAI(mailboxAuthed, &mailboxNativeAI, mailboxAI, aiSearchAsk)
			// AI-005 server-authoritative tenant budget visibility.
			mailboxAuthed.GET("/ai/budget", aiBudgetStatus.BudgetStatus)
			handlers.RegisterCalendarReminderRoutes(mailboxAuthed, calendarHandler)
			handlers.RegisterCalendarIncomingRoutes(mailboxAuthed, calendarHandler)
			mailboxAuthed.GET("/calendars/events", calendarHandler.ListEvents)
			mailboxAuthed.POST("/calendars/events", calendarHandler.CreateEvent)
			mailboxAuthed.POST("/calendars/events/:id/invitations", calendarHandler.SendInvitations)
			mailboxAuthed.POST("/calendars/events/:id/reply", calendarHandler.IngestReply)
			mailboxAuthed.PUT("/calendars/events/:id", calendarHandler.UpdateEvent)
			mailboxAuthed.DELETE("/calendars/events/:id", calendarHandler.DeleteEvent)
			mailboxAuthed.GET("/calendars/events/:id/ics", calendarHandler.ExportEvent)
			mailboxAuthed.GET("/calendars/export.ics", calendarHandler.ExportCalendarICS)
			mailboxAuthed.POST("/calendars/import", calendarHandler.ImportEvent)
			mailboxAuthed.DELETE("/session", mailboxNative.DeleteSession)
		}
		v1.POST("/mailbox/sieve/exchange", sieveBridge.Exchange)
		v1.GET("/mailbox/sieve/session", sieveBridge.Session)
		v1.PUT("/mailbox/sieve/rules", sieveBridge.Save)
		v1.POST("/mailbox/sieve/logout", sieveBridge.Logout)
		v1.GET("/branding", brandingHandler.Public)
		v1.GET("/i18n/:locale", i18nCatalog(opts))

		v1.POST("/marketing/unsubscribe", marketingSuppressionHandler.OneClick)
		v1.GET("/marketing/unsubscribe", marketingSuppressionHandler.Confirmation)
		v1.POST("/marketing/channels/:channel_id/feedback", marketingHandler.Feedback)

		// Public Auth
		authGroup := v1.Group("/auth")
		{
			authGroup.POST("/login", middleware.RateLimitWindow(20, time.Minute, func(c *gin.Context) (string, bool) { return c.ClientIP(), true }), authHandler.Login)
			// Second MFA login leg: authenticated by the short-lived
			// one-time mfa_token, not a session.
			authGroup.POST("/mfa/verify", authHandler.MFAVerify)
		}

		// Legacy Mail-in-a-Box API. Basic auth; platform administrators only.
		if authQueries != nil && platformQueries != nil {
			legacy := v1.Group("/admin")
			{
				legacy.GET("/system/status", legacyHandler.Status)
				legacy.GET("/system/version", legacyHandler.VersionInfo)
				legacy.GET("/mail/users", legacyHandler.MailUsers)
				legacy.GET("/mail/domains", legacyHandler.MailDomains)
				legacy.GET("/mail/aliases", legacyHandler.MailAliases)
				legacy.GET("/dns/zones", legacyHandler.DNSZones)
				legacy.POST("/mail/users/add", legacyHandler.AddMailUser)
				legacy.POST("/mail/aliases/add", legacyHandler.AddMailAlias)
				legacy.POST("/mail/users/remove", legacyHandler.RemoveMailUser)
				legacy.POST("/mail/users/password", legacyHandler.SetMailUserPassword)
				legacy.POST("/mail/aliases/remove", legacyHandler.RemoveMailAlias)
				legacy.GET("/ssl/status", legacyHandler.SSLStatus)
				legacy.GET("/dns/zonefile/:zone", legacyHandler.ZoneFile)
			}
		}

		// Platform administration. These routes deliberately run outside
		// RequireTenantTx: the platform role is cross-tenant by design.
		if authQueries != nil && platformQueries != nil {
			// Operator telemetry sits behind the platform admin session.
			metrics := engine.Group("")
			metrics.Use(middleware.RequireAuth(authQueries))
			metrics.Use(middleware.RequirePlatformAdmin(authQueries))
			metrics.GET("/metrics", gin.WrapH(promhttp.Handler()))

			platform := v1.Group("/platform")
			platform.Use(middleware.AuditPlatform(opts.Audit, opts.Logger))
			platform.Use(middleware.RequireAuth(authQueries))
			platform.Use(middleware.RequirePlatformAdmin(authQueries))
			platform.Use(middleware.RequireCSRF())
			{
				platform.GET("/overview", platformHandler.Overview)
				platform.GET("/settings", platformHandler.Settings)
				platform.PATCH("/settings", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.UpdateSettings)
				platform.GET("/tenants", platformHandler.ListTenants)
				platform.POST("/tenants", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.CreateTenant)
				platform.GET("/brands/:id/settings", platformHandler.BrandSettings)
				platform.PATCH("/brands/:id/settings", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.UpdateBrandSettings)
				platform.GET("/brands", brandingHandler.List)
				platform.POST("/brands", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), brandingHandler.Save)
				platform.GET("/brand-hosts", brandingHandler.Hosts)
				platform.GET("/brand-hosts/:hostname/lifecycle", (&handlers.BrandHostLifecycleHandler{Pool: opts.PlatformPool, Agent: opts.DNSAuthorityAgent}).Status)
				platform.POST("/brand-hosts/:hostname/lifecycle", (&handlers.BrandHostLifecycleHandler{Pool: opts.PlatformPool, Agent: opts.DNSAuthorityAgent, Reconcile: opts.Reconcile, LookupTXT: opts.BrandHostTXTLookup}).Action)
				platform.POST("/brand-hosts", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), brandingHandler.BindHost)
				platform.DELETE("/brand-hosts/:hostname", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), brandingHandler.UnbindHost)
				platform.PATCH("/brands/:id/status", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), brandingHandler.SetStatus)
				platform.DELETE("/brands/:id", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), brandingHandler.Delete)
				platform.POST("/brands/:id/reset-theme", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), brandingHandler.ResetTheme)
				platform.GET("/channels", platformHandler.Channels)
				platform.GET("/audit", platformHandler.AuditLogs)
				platform.GET("/support/tenants/:id/attachments/:attachment_id", supportHandler.PlatformAttachment)
				platform.GET("/support/tickets", supportHandler.PlatformList)
				platform.GET("/support/tenants/:id/tickets/:ticket_id", supportHandler.PlatformGet)
				platform.POST("/support/tenants/:id/tickets/:ticket_id/messages", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), supportHandler.PlatformReply)
				platform.PATCH("/support/tenants/:id/tickets/:ticket_id/status", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), supportHandler.PlatformUpdate)
				platform.GET("/support/announcements", supportHandler.PlatformAnnouncements)
				platform.POST("/support/announcements", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), supportHandler.SaveAnnouncement)
				platform.PATCH("/support/announcements/:announcement_id", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), supportHandler.SaveAnnouncement)

				platform.GET("/alerts", platformNotifications.Alerts)
				platform.GET("/tenants/:id/ai/configuration", aiConfiguration.PlatformGet)
				platform.POST("/tenants/:id/ai/configuration", aiConfiguration.PlatformPut)
				platform.GET("/tenants/:id/ai/usage", aiConfiguration.PlatformUsage)
				platform.POST("/tenants/:id/ai/health", middleware.RateLimitWindow(5, time.Minute, middleware.TenantRateKey), aiConfiguration.PlatformHealth)
				platform.POST("/tenants/:id/ai/usage/:request_id/resolve", aiConfiguration.ResolveUsage)
				platform.GET("/tenants/:id/ai/budget", aiConfiguration.PlatformBudgetGet)
				platform.POST("/tenants/:id/ai/budget", aiConfiguration.PlatformBudgetPut)
				platform.PUT("/tenants/:id/ai", aiConfiguration.PlatformPut)
				platform.POST("/tenants/:id/ai/requests/:request_id/resolve", aiConfiguration.ResolveUsage)
				platform.GET("/reminder-policy", (&handlers.NotificationConfigHandler{Store: platformQueries, Secrets: opts.Secrets}).ReminderPolicy)
				platform.PUT("/reminder-policy", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), (&handlers.NotificationConfigHandler{Store: platformQueries, Secrets: opts.Secrets}).SaveReminderPolicy)
				platform.PUT("/notification-channel", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), (&handlers.NotificationConfigHandler{Store: platformQueries, Secrets: opts.Secrets}).Save)
				offsiteBackup := &handlers.BackupOffsiteAdminHandler{Store: platformQueries, Secrets: opts.Secrets}
				platform.GET("/backup/offsite", offsiteBackup.Get)
				platform.PUT("/backup/offsite", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), offsiteBackup.Put)
				platform.POST("/backup/offsite/test-connection", offsiteBackup.TestConnection)
				platform.POST("/channels", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.UpsertChannel)
				platform.POST("/tenants/:id/suspend", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.SuspendTenant)
				platform.POST("/tenants/:id/activate", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.ActivateTenant)
				platform.POST("/tenants/:id/close", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.CloseTenant)
				platform.PATCH("/tenants/:id", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.UpdateTenant)
				platform.POST("/tenants/:id/owner", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.TransferOwner)
				platform.POST("/tenants/:id/marketing/channel", middleware.RequirePlatformTx(opts.MarketingPlatformPool, opts.Logger), marketingHandler.ConfigureChannel)
				platform.PATCH("/tenants/:id/marketing/channels/:channel_id", middleware.RequirePlatformTx(opts.MarketingPlatformPool, opts.Logger), marketingHandler.ConfigureChannel)
				platform.POST("/tenants/:id/marketing/recipients/:recipient_id/resolve", middleware.RequirePlatformTx(opts.MarketingPlatformPool, opts.Logger), marketingHandler.ResolveAttempt)
				platform.GET("/mail/queue", deliveryHandler.PlatformQueue)
				platform.GET("/mail/collectors", deliveryHandler.CollectorHealth)
			}
		}

		// User-level routes need a session but not a tenant transaction.
		// Keeping logout/me/switch here means a user removed from their
		// current tenant can still switch or sign out instead of getting a
		// permanent 403 from RequireTenantTx.
		userLevel := v1.Group("")
		userLevel.Use(middleware.RequireAuth(authQueries))
		userLevel.Use(middleware.RequireCSRF())
		{
			userLevel.POST("/auth/logout", authHandler.Logout)
			userLevel.GET("/auth/me", authHandler.Me)

			// MFA management (any logged-in user, role-independent)
			userLevel.GET("/auth/mfa/status", authHandler.MFAStatus)
			userLevel.POST("/auth/mfa/setup", authHandler.MFASetup)
			userLevel.POST("/auth/mfa/enable", authHandler.MFAEnable)
			userLevel.POST("/auth/mfa/disable", authHandler.MFADisable)

			userLevel.POST("/auth/switch-tenant", authHandler.SwitchTenant)
			userLevel.POST("/auth/password", authHandler.ChangePassword)
			userLevel.POST("/auth/legacy-session", authHandler.CreateLegacySession)
			userLevel.GET("/auth/application-credentials", authHandler.ListApplicationCredentials)
			userLevel.POST("/auth/application-credentials", authHandler.CreateApplicationCredential)
			userLevel.DELETE("/auth/application-credentials/:id", authHandler.RevokeApplicationCredential)
			userLevel.GET("/auth/sessions", authHandler.ListSessions)
			userLevel.DELETE("/auth/sessions/:id", authHandler.RevokeSession)
		}

		// Tenant-scoped routes run inside the RLS transaction.
		// Provider reads finish before acquiring the final audited tenant transaction.
		dnsPrepared := v1.Group("")
		if opts.Audit != nil {
			dnsPrepared.Use(middleware.Audit(opts.Audit, opts.Logger))
		}
		dnsPrepared.Use(middleware.RequireAuth(authQueries), middleware.RequireCSRF(), middleware.RateLimitWindow(5, time.Minute, middleware.TenantRateKey), dnsPlanHandler.Preflight, middleware.RequireTenantTx(opts.AppPool, opts.Logger), middleware.RequireRole("owner", "admin"))
		dnsPrepared.PUT("/domains/:id/dns/credentials", dnsPlanHandler.SaveCredential)
		dnsPrepared.POST("/domains/:id/dns/plans", dnsPlanHandler.Preview)
		dnsPrepared.POST("/domains/:id/dns/plans/:plan/reconcile", dnsPlanHandler.Reconcile)

		// Network probes own short repository transactions, not a request-long tx.
		probes := v1.Group("")
		probes.Use(middleware.RequireAuth(authQueries), middleware.RequireCSRF(), middleware.RateLimitWindow(5, time.Minute, middleware.TenantRateKey))
		probes.POST("/domains/:id/verify", domainHandler.Verify)
		probes.POST("/ai/health", aiConfiguration.Health)

		aiProtected := workloadTenantGroup(v1, opts, authQueries, opts.AIAppPool)
		marketingProtected := workloadTenantGroup(v1, opts, authQueries, opts.MarketingAppPool)
		protected := v1.Group("")
		if opts.Audit != nil {
			protected.Use(middleware.Audit(opts.Audit, opts.Logger))
		}
		protected.Use(middleware.RequireAuth(authQueries))
		protected.Use(middleware.RequireTenantTx(opts.AppPool, opts.Logger))
		protected.Use(middleware.RequireCSRF())
		handlers.RegisterArchiveCaseRoutes(protected, opts.AppPool, opts.PlatformPool, opts.ArchiveRoot)
		handlers.RegisterMarketingFlowRoutes(marketingProtected, marketingAppQueries, opts.MarketingEnabled && !opts.DevMode && !opts.Quarantined)
		handlers.RegisterMarketingReportRoutes(marketingProtected, marketingAppQueries)
		{
			// Tenants
			protected.GET("/tenants/current", tenantHandler.Current)
			protected.GET("/onboarding/tasks", onboardingHandler.List)
			protected.GET("/domains/:id/dns/credentials", middleware.RequireRole("owner", "admin"), dnsPlanHandler.Credentials)
			protected.DELETE("/domains/:id/dns/credentials", middleware.RequireRole("owner", "admin"), middleware.RateLimitWindow(5, time.Minute, middleware.TenantRateKey), dnsPlanHandler.RevokeCredential)
			protected.GET("/domains/:id/dns/plans", middleware.RequireRole("owner", "admin"), dnsPlanHandler.Plans)
			protected.POST("/domains/:id/dns/plans/:plan/confirm", middleware.RequireRole("owner", "admin"), middleware.RateLimitWindow(5, time.Minute, middleware.TenantRateKey), dnsPlanHandler.Confirm)
			protected.POST("/domains/:id/dns/plans/:plan/cancel", middleware.RequireRole("owner", "admin"), middleware.RateLimitWindow(5, time.Minute, middleware.TenantRateKey), dnsPlanHandler.Cancel)
			protected.POST("/domains/:id/dns/plans/:plan/rollback", middleware.RequireRole("owner", "admin"), middleware.RateLimitWindow(5, time.Minute, middleware.TenantRateKey), dnsPlanHandler.Rollback)
			protected.POST("/domains/:id/dns/plans/:plan/abandon", middleware.RequireRole("owner", "admin"), middleware.RateLimitWindow(5, time.Minute, middleware.TenantRateKey), dnsPlanHandler.Abandon)
			protected.POST("/domains/:id/onboarding/retry", middleware.RequireRole("owner", "admin"), middleware.RateLimitWindow(5, time.Minute, middleware.TenantRateKey), dnsPlanHandler.Retry)
			protected.PATCH("/tenants/current", middleware.RequireRole("owner", "admin"), tenantHandler.UpdateSettings)

			// Domains
			handlers.RegisterAdminCorporateDirectoryRoutes(protected, &handlers.MailboxCorporateDirectoryHandler{Pool: opts.AppPool})
			protected.GET("/domains", domainHandler.List)
			protected.POST("/domains", middleware.RequireRole("owner", "admin"), domainHandler.Create)
			protected.POST("/domains/:id/challenge", middleware.RequireRole("owner", "admin"), middleware.RateLimitWindow(5, time.Minute, middleware.TenantRateKey), domainHandler.ReissueChallenge)
			protected.PATCH("/domains/:id", middleware.RequireRole("owner", "admin"), domainHandler.Update)
			protected.DELETE("/domains/:id", middleware.RequireRole("owner", "admin"), domainHandler.Delete)

			// Mailboxes
			protected.GET("/mailboxes", mailboxHandler.List)
			protected.POST("/mailboxes", middleware.RequireRole("owner", "admin"), mailboxHandler.Create)
			protected.POST("/mailboxes/:id/password", middleware.RequireRole("owner", "admin"), mailboxHandler.UpdatePassword)
			protected.POST("/mailboxes/:id/quota", middleware.RequireRole("owner", "admin"), mailboxHandler.UpdateQuota)
			protected.PATCH("/mailboxes/:id", middleware.RequireRole("owner", "admin"), mailboxHandler.UpdateSettings)
			protected.DELETE("/mailboxes/:id", middleware.RequireRole("owner", "admin"), mailboxHandler.Delete)

			// Aliases
			protected.GET("/aliases", aliasHandler.List)
			protected.POST("/aliases", middleware.RequireRole("owner", "admin"), aliasHandler.Create)
			protected.PATCH("/aliases/:id", middleware.RequireRole("owner", "admin"), aliasHandler.Update)
			protected.DELETE("/aliases/:id", middleware.RequireRole("owner", "admin"), aliasHandler.Delete)

			protected.GET("/audit", auditHandler.List)
			protected.GET("/audit/export", middleware.RequireRole("owner", "admin"), auditHandler.Export)
			protected.GET("/compliance/grants", middleware.RequireRole("owner"), complianceGrantHandler.List)
			protected.POST("/compliance/grants", middleware.RequireRole("owner"), middleware.RateLimitWindow(10, time.Minute, middleware.TenantRateKey), complianceGrantHandler.Create)
			protected.POST("/compliance/grants/:id/revoke", middleware.RequireRole("owner"), complianceGrantHandler.Revoke)
			protected.GET("/mail/health", middleware.RequireRole("owner", "admin"), deliveryHandler.Health)
			protected.GET("/mail/queue", middleware.RequireRole("owner", "admin"), middleware.RateLimitWindow(5, time.Minute, middleware.TenantRateKey), deliveryHandler.QueueForTenant)
			marketingProtected.GET("/marketing/campaigns/:campaign_id/preview", middleware.RequireRole("owner", "admin"), marketingHandler.PreviewCampaign)
			marketingProtected.GET("/marketing/campaigns/:campaign_id/report", middleware.RequireRole("owner", "admin"), marketingHandler.CampaignReport)
			protected.GET("/notifications", notificationHandler.List)
			aiProtected.POST("/ai/consent", middleware.RequireRole("owner", "admin"), middleware.RateLimitWindow(10, time.Minute, middleware.TenantRateKey), aiConfiguration.Consent)
			aiProtected.GET("/ai/budget", middleware.RequireRole("owner", "admin"), aiConfiguration.TenantBudgetGet)
			aiProtected.GET("/ai/configuration", middleware.RequireRole("owner", "admin"), aiConfiguration.Get)
			aiProtected.PUT("/ai/configuration", middleware.RequireRole("owner", "admin"), aiConfiguration.Put)
			aiProtected.GET("/ai/usage", middleware.RequireRole("owner", "admin"), aiConfiguration.Usage)
			marketingProtected.GET("/marketing/channels", middleware.RequireRole("owner", "admin"), marketingHandler.Channels)
			marketingProtected.GET("/marketing/templates", middleware.RequireRole("owner", "admin"), marketingHandler.Templates)
			marketingProtected.POST("/marketing/templates", middleware.RequireRole("owner", "admin"), marketingHandler.CreateTemplate)
			marketingProtected.GET("/marketing/lists", middleware.RequireRole("owner", "admin"), marketingHandler.Lists)
			marketingProtected.POST("/marketing/lists", middleware.RequireRole("owner", "admin"), marketingHandler.CreateList)
			marketingProtected.GET("/marketing/lists/:list_id/contacts", middleware.RequireRole("owner", "admin"), marketingHandler.Contacts)
			marketingProtected.POST("/marketing/lists/:list_id/contacts", middleware.RequireRole("owner", "admin"), marketingHandler.ImportContacts)
			marketingProtected.GET("/marketing/campaigns", middleware.RequireRole("owner", "admin"), marketingHandler.Campaigns)
			marketingProtected.POST("/marketing/campaigns", middleware.RequireRole("owner", "admin"), marketingHandler.CreateCampaign)
			marketingProtected.POST("/marketing/campaigns/:campaign_id/confirm", middleware.RequireRole("owner", "admin"), marketingHandler.ConfirmCampaign)
			marketingProtected.PATCH("/marketing/campaigns/:campaign_id", middleware.RequireRole("owner", "admin"), marketingHandler.ChangeCampaign)
			marketingProtected.GET("/marketing/campaigns/:campaign_id/recipients", middleware.RequireRole("owner", "admin"), marketingHandler.Recipients)
			marketingProtected.GET("/marketing/suppressions", middleware.RequireRole("owner", "admin"), marketingSuppressionHandler.List)
			marketingProtected.POST("/marketing/suppressions", middleware.RequireRole("owner", "admin"), marketingSuppressionHandler.Add)
			marketingProtected.DELETE("/marketing/suppressions", middleware.RequireRole("owner", "admin"), marketingSuppressionHandler.Remove)

			// Archive / legal hold
			protected.GET("/archive", archiveHandler.List)
			protected.GET("/archive/search", middleware.RequireRole("owner", "admin"), archiveContent.Search)
			protected.POST("/archive/:id/restore", middleware.RequireRole("owner", "admin"), archiveContent.Restore)
			protected.GET("/archive/:id/attachments/:attachment", middleware.RequireRole("owner", "admin"), archiveContent.Attachment)
			protected.GET("/archive/:id/content", middleware.RequireRole("owner", "admin"), archiveContent.Content)
			protected.GET("/archive/:id/export", middleware.RequireRole("owner", "admin"), archiveContent.Export)
			protected.GET("/archive/evidence/:id", archiveEvidence.Download)
			protected.GET("/archive/retention", middleware.RequireRole("owner", "admin"), archiveContent.RetentionStatus)
			protected.DELETE("/archive/retention", middleware.RequireRole("owner", "admin"), archiveContent.RetentionDisable)
			protected.POST("/archive/retention/preview", middleware.RequireRole("owner", "admin"), archiveContent.RetentionPreview)
			protected.POST("/archive/retention/confirm", middleware.RequireRole("owner", "admin"), archiveContent.RetentionConfirm)
			protected.GET("/support/tickets", supportHandler.List)
			protected.POST("/support/tickets", supportHandler.Create)
			protected.GET("/support/tickets/:ticket_id", supportHandler.Get)
			protected.POST("/support/tickets/:ticket_id/messages", supportHandler.Reply)
			protected.PATCH("/support/tickets/:ticket_id/status", supportHandler.Status)
			protected.GET("/support/attachments/:attachment_id", supportHandler.Attachment)
			protected.GET("/support/announcements", supportHandler.Announcements)
			handlers.RegisterDNSAuthorityRoutes(protected, &handlers.DNSAuthorityHandler{App: opts.AppPool, Agent: opts.DNSAuthorityAgent, Reconcile: opts.Reconcile})
			protected.GET("/support/help", supportHandler.Help)
			protected.GET("/domains/:id/transport-security", middleware.RequireRole("owner", "admin"), transportHandler.Get)
			protected.PUT("/domains/:id/transport-security", middleware.RequireRole("owner", "admin"), transportHandler.Publish)
			protected.DELETE("/domains/:id/transport-security", middleware.RequireRole("owner", "admin"), transportHandler.Disable)
			protected.POST("/domains/:id/transport-security/observe", middleware.RequireRole("owner", "admin"), transportHandler.Observe)
			protected.GET("/domains/:id/transport-security/reports", middleware.RequireRole("owner", "admin"), transportHandler.Reports)
			protected.POST("/support/diagnostics", middleware.RequireRole("owner", "admin"), supportHandler.Diagnostics)

			protected.POST("/compliance/recalls", middleware.RequireRole("owner", "admin"), recallHandler.Create)
			protected.GET("/compliance/recalls", middleware.RequireRole("owner", "admin"), recallHandler.List)
			protected.GET("/compliance/recalls/:id", middleware.RequireRole("owner", "admin"), recallHandler.Get)
			handlers.RegisterRecallProductRoutes(protected, recallHandler)
			protected.POST("/archive/:id/hold", middleware.RequireRole("owner", "admin"), archiveHandler.SetHold)
			protected.POST("/archive/:id/release", middleware.RequireRole("owner", "admin"), archiveHandler.ClearHold)
			protected.DELETE("/archive/:id", middleware.RequireRole("owner", "admin"), archiveHandler.Delete)

		}
	}

	// The legacy Mail-in-a-Box UI calls /admin/* directly, outside /api/v1.
	if authQueries != nil && platformQueries != nil {
		legacyRoot := engine.Group("/admin")
		{
			legacyRoot.GET("/system/status", legacyHandler.Status)
			legacyRoot.GET("/system/version", legacyHandler.VersionInfo)
			legacyRoot.GET("/mail/users", legacyHandler.MailUsers)
			legacyRoot.GET("/mail/domains", legacyHandler.MailDomains)
			legacyRoot.GET("/mail/aliases", legacyHandler.MailAliases)
			legacyRoot.GET("/dns/zones", legacyHandler.DNSZones)
			legacyRoot.POST("/mail/users/add", legacyHandler.AddMailUser)
			legacyRoot.POST("/mail/aliases/add", legacyHandler.AddMailAlias)
			legacyRoot.POST("/mail/users/remove", legacyHandler.RemoveMailUser)
			legacyRoot.POST("/mail/users/password", legacyHandler.SetMailUserPassword)
			legacyRoot.POST("/mail/aliases/remove", legacyHandler.RemoveMailAlias)
			legacyRoot.GET("/ssl/status", legacyHandler.SSLStatus)
			legacyRoot.GET("/system/updates", legacyHandler.Updates)
			legacyRoot.GET("/system/backup/status", legacyHandler.BackupStatus)
			legacyRoot.GET("/web/domains", legacyHandler.WebDomains)
			legacyRoot.GET("/mfa/status", legacyHandler.MFAStatus)
			legacyRoot.GET("/system/latest-upstream-version", legacyHandler.LatestVersion)
			legacyRoot.GET("/system/privacy", legacyHandler.Privacy)
			legacyRoot.GET("/dns/custom", legacyHandler.EmptyList)
			legacyRoot.GET("/mail/users/privileges", legacyHandler.EmptyList)
			legacyRoot.POST("/system/reboot", legacyHandler.Unsupported)
			legacyRoot.POST("/system/update-packages", legacyHandler.Unsupported)
			legacyRoot.POST("/ssl/install", legacyHandler.Unsupported)
			legacyRoot.POST("/ssl/provision", legacyHandler.Unsupported)
			legacyRoot.POST("/web/update", legacyHandler.Unsupported)
			legacyRoot.POST("/dns/update", legacyHandler.Unsupported)
			legacyRoot.POST("/mail/users/privileges/add", legacyHandler.Unsupported)
			legacyRoot.GET("/dns/zonefile/:zone", legacyHandler.ZoneFile)
		}
	}

	// Single-Page Application Fallback
	engine.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") || strings.HasPrefix(c.Request.URL.Path, "/admin/") ||
			(c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead) {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND"}})
			return
		}
		if web.IsAssetPath(c.Request.URL.Path) {
			c.Status(http.StatusNotFound)
			return
		}
		web.ServeIndex(c)
	})

	return engine
}

// healthz reports whether the process can serve requests.
func healthz(opts Options) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()

		status := gin.H{"status": "ok", "version": opts.Version, "mode": "production", "quarantined": opts.Quarantined, "workers_held": opts.WorkersHeld}
		if opts.DevMode {
			status["mode"] = "development"
		}
		if err := ping(ctx, opts.AppPool); err != nil {
			status["status"] = "degraded"
			status["db"] = "unavailable"
			c.JSON(http.StatusServiceUnavailable, status)
			return
		}
		status["db"] = "ok"
		if opts.SieveGateReady != nil {
			status["sieve_outbound_gate_ready"] = opts.SieveGateReady()
			if status["sieve_outbound_gate_ready"] == false {
				status["status"] = "degraded"
				c.JSON(http.StatusServiceUnavailable, status)
				return
			}
		}
		c.JSON(http.StatusOK, status)
	}
}

func ping(ctx context.Context, pool *db.Pool) error {
	if pool == nil {
		return errors.New("database pool is not configured")
	}
	return pool.Ping(ctx)
}

func i18nCatalog(opts Options) gin.HandlerFunc {
	return func(c *gin.Context) {
		locale := c.Param("locale")
		if locale == "" {
			locale = i18n.DefaultLocale
		}
		c.JSON(http.StatusOK, opts.Messages.Raw(locale))
	}
}

// protocolEnabled gives callers with pre-existing Options literal values the
// same behavior, while configuration-loaded false remains unambiguous.
func protocolEnabled(value *bool) bool { return value == nil || *value }

func collaborationProtocolGate(opts Options) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		card := path == "/.well-known/carddav" || path == "/api/v1/carddav" || strings.HasPrefix(path, "/api/v1/carddav/") || strings.HasPrefix(path, "/api/v1/mailbox/carddav/")
		calendar := path == "/.well-known/caldav" || path == "/caldav" || strings.HasPrefix(path, "/caldav/") || path == "/api/v1/caldav" || strings.HasPrefix(path, "/api/v1/caldav/") || strings.HasPrefix(path, "/api/v1/mailbox/caldav/")
		easRequest := path == "/Microsoft-Server-ActiveSync" || path == "/api/v1/eas" || path == "/api/v1/mailbox/eas"
		if (card && !protocolEnabled(opts.CardDAVEnabled)) || (calendar && !protocolEnabled(opts.CalDAVEnabled)) || (easRequest && !protocolEnabled(opts.EASEnabled)) {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.Next()
	}
}

// Package api wires the HTTP surface of urMail.cn.
package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/handlers"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/dnsprovider"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/onboarding"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/refund"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/web"
)

// Options carries the dependencies of the HTTP server.
type Options struct {
	DevMode     bool
	Quarantined bool
	DNSPlans    *onboarding.Service
	Logger      *zap.Logger
	// AppPool is the tenant-scoped pool.
	AppPool *db.Pool
	// PlatformPool is the platform-scoped pool.
	PlatformPool *db.Pool
	// Messages is the message catalog.
	Messages *i18n.Store
	// Version is reported by /api/v1/healthz.
	Version string
	// MyHostname is the public MX host advertised in domain DNS guidance.
	MyHostname string
	// Reconcile is the configuration reconcile trigger.
	Reconcile handlers.ReconcileTrigger
	// Recall processes persisted local mailbox recalls through the root agent.
	Recall              handlers.RecallExpunger
	Queue               handlers.QueueReader
	TelemetryStream     string
	RoundcubeBridgeKey  string
	RoundcubeEnabled    bool
	RegistrationEnabled bool
	AICloudEndpoints    []string
	AILocalEndpoints    []string
	SieveActivator      handlers.SieveActivator
	MetricsToken        string
	// Audit receives one row per successful mutating tenant request.
	Audit middleware.AuditWriter
	// Secrets encrypts TOTP seeds at rest. Nil keeps legacy plaintext support.
	Secrets handlers.SecretsBox
	// DNSProvider writes verification records for delegated domains when set.
	DNSProvider dnsprovider.Provider
	// DNSManagedZones allow-lists the zones DNSProvider may write; a domain
	// outside them verifies manually (M5 review 3.11).
	DNSManagedZones []string
	// Payment enables Stripe Checkout and webhook settlement when set.
	Payment *payment.Stripe
	// QuoteHMACKey signs client-visible billing quotes; empty disables issuance.
	QuoteHMACKey string
	// BaseURL is the public panel origin used for Stripe redirects.
	BaseURL string
	// MarketingTokens verifies stateless, signed unsubscribe tokens.
	MarketingTokens interface {
		VerifyUnsubscribeToken(string) (int64, string, bool)
	}
}

var quarantineLegacyQuery = regexp.MustCompile(`^/api/v1/platform/orders/[1-9][0-9]{0,18}/query$`)

var quarantineRefundQuery = regexp.MustCompile(`^/api/v1/platform/refunds/[1-9][0-9]{0,18}/query$`)

var quarantinePaymentQuery = regexp.MustCompile(`^/api/v1/platform/payment-merchants/[1-9][0-9]{0,18}/orders/[1-9][0-9]{0,18}/query$`)

// New builds the gin engine with all middleware and routes attached.
func New(opts Options) *gin.Engine {
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	gin.SetMode(gin.ReleaseMode)

	engine := gin.New()

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
			reconcilePayment := c.Request.Method == http.MethodPost && (quarantinePaymentQuery.MatchString(c.Request.URL.Path) || quarantineRefundQuery.MatchString(c.Request.URL.Path) || quarantineLegacyQuery.MatchString(c.Request.URL.Path))
			if readOnly || authException || reconcilePayment {
				c.Next()
				return
			}
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "RESTORE_QUARANTINED", "message": "error.RESTORE_QUARANTINED"}})
		})
	}

	engine.Use(middleware.BodyLimit(1 << 20))
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
		authQueries = store.New(opts.PlatformPool.Pool)
	}
	var appQueries *store.Queries
	if opts.AppPool != nil {
		appQueries = store.New(opts.AppPool.Pool)
	}

	authHandler := handlers.NewAuthHandler(authQueries)
	authHandler.Secrets = opts.Secrets
	var platformQueries *store.Queries
	if opts.PlatformPool != nil {
		platformQueries = store.New(opts.PlatformPool.Pool)
	}
	tenantHandler := handlers.NewTenantHandler(appQueries, platformQueries)
	onboardingHandler := &handlers.OnboardingHandler{Store: appQueries}
	dnsPlanHandler := &handlers.DNSPlanHandler{Store: appQueries, Service: opts.DNSPlans}
	domainHandler := handlers.NewDomainHandler(appQueries, opts.Reconcile)
	domainHandler.DNS = opts.DNSProvider
	domainHandler.DNSManagedZones = opts.DNSManagedZones
	domainHandler.Hostname = opts.MyHostname
	mailboxHandler := handlers.NewMailboxHandler(appQueries, opts.Reconcile)
	aliasHandler := handlers.NewAliasHandler(appQueries, opts.Reconcile)
	archiveHandler := handlers.NewArchiveHandler(appQueries)
	archiveContent := &handlers.ArchiveContentHandler{Store: appQueries}
	refundAdmin := &handlers.RefundAdminHandler{Store: platformQueries}
	invoiceProfile := &handlers.InvoiceProfileHandler{Store: appQueries, Platform: platformQueries}
	supportHandler := &handlers.SupportHandler{Store: appQueries, Platform: platformQueries, PlatformPool: opts.PlatformPool}
	recallHandler := &handlers.RecallHandler{Store: appQueries, Platform: platformQueries, Expunger: opts.Recall}
	auditHandler := handlers.NewAuditHandler(appQueries)
	complianceGrantHandler := &handlers.ComplianceGrantHandler{Store: appQueries}
	billingHandler := handlers.NewBillingHandler(appQueries)
	billingHandler.Pool = opts.PlatformPool
	billingHandler.Stripe = opts.Payment
	billingHandler.BaseURL = opts.BaseURL
	billingHandler.QuoteHMACKey = []byte(opts.QuoteHMACKey)
	brandingHandler := handlers.NewBrandingHandler(platformQueries)
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
	registrationHandler := &handlers.RegistrationHandler{Pool: opts.PlatformPool, Secrets: opts.Secrets, BaseURL: opts.BaseURL, Enabled: opts.RegistrationEnabled}
	bridgeKey := []byte(opts.RoundcubeBridgeKey)
	if !opts.RoundcubeEnabled {
		bridgeKey = nil
	}
	sieveBridge := &handlers.SieveBridgeHandler{Pool: opts.PlatformPool, Key: bridgeKey, Activator: opts.SieveActivator}
	paymentGateway := &handlers.PaymentGatewayHandler{Quarantined: opts.Quarantined, LegacyStripe: opts.Payment, Registry: &payment.Registry{Store: platformQueries, Secrets: opts.Secrets, BaseURL: opts.BaseURL}, Platform: platformQueries, Pool: opts.PlatformPool, Secrets: opts.Secrets}
	billingHandler.Gateway = paymentGateway
	mailboxAI := &handlers.MailboxAIService{Pool: opts.PlatformPool, Secrets: opts.Secrets, CloudEndpoints: opts.AICloudEndpoints, LocalEndpoints: opts.AILocalEndpoints}
	aiConfiguration := &handlers.AIConfigurationHandler{Pool: opts.PlatformPool, Secrets: opts.Secrets, CloudEndpoints: opts.AICloudEndpoints, LocalEndpoints: opts.AILocalEndpoints}
	mailboxBridge := &handlers.MailboxBridgeHandler{Pool: opts.PlatformPool, Key: bridgeKey, AI: mailboxAI, Sieve: opts.SieveActivator}
	deliveryHandler := &handlers.DeliveryHandler{Store: appQueries, Platform: platformQueries, Queue: opts.Queue, Stream: opts.TelemetryStream}
	marketingHandler := &handlers.MarketingHandler{Store: appQueries, Platform: platformQueries, Secrets: opts.Secrets}
	marketingSuppressionHandler := &handlers.MarketingSuppressionHandler{Store: appQueries, Tokens: opts.MarketingTokens, AppPool: opts.AppPool, Messages: opts.Messages}

	engine.GET("/internal/metrics", middleware.MetricsAccess(opts.MetricsToken), gin.WrapH(promhttp.Handler()))
	v1 := engine.Group("/api/v1")
	{
		v1.GET("/healthz", healthz(opts))
		v1.GET("/roundcube/status", func(c *gin.Context) { c.JSON(200, gin.H{"enabled": opts.RoundcubeEnabled}) })
		v1.POST("/roundcube/bridge", mailboxBridge.Bridge)
		v1.POST("/mailbox/sieve/exchange", sieveBridge.Exchange)
		v1.GET("/mailbox/sieve/session", sieveBridge.Session)
		v1.PUT("/mailbox/sieve/rules", sieveBridge.Save)
		v1.POST("/mailbox/sieve/logout", sieveBridge.Logout)
		v1.GET("/public/plans", (&handlers.PublicPricingHandler{Store: appQueries}).Plans)
		v1.GET("/branding", brandingHandler.Public)
		v1.GET("/i18n/:locale", i18nCatalog(opts))

		v1.POST("/billing/webhook/stripe", billingHandler.StripeWebhook)
		v1.POST("/payments/:merchant_id/webhook", middleware.RateLimitWindow(60, time.Minute, func(c *gin.Context) (string, bool) {
			id, err := strconv.ParseInt(c.Param("merchant_id"), 10, 64)
			if err != nil || id <= 0 {
				return c.ClientIP() + "|invalid", true
			}
			return c.ClientIP() + "|" + strconv.FormatInt(id, 10), true
		}), paymentGateway.Webhook)
		v1.POST("/marketing/unsubscribe", marketingSuppressionHandler.OneClick)
		v1.GET("/marketing/unsubscribe", marketingSuppressionHandler.Confirmation)
		v1.POST("/marketing/channels/:channel_id/feedback", marketingHandler.Feedback)

		// Public Auth
		authGroup := v1.Group("/auth")
		{
			authGroup.GET("/registration-status", registrationHandler.Status)
			authGroup.POST("/login", authHandler.Login)
			authGroup.POST("/register", middleware.RateLimitWindow(3, time.Hour, func(c *gin.Context) (string, bool) { return c.ClientIP(), true }), registrationHandler.Register)
			authGroup.POST("/register/verify", middleware.RateLimitWindow(10, time.Minute, func(c *gin.Context) (string, bool) { return c.ClientIP(), true }), registrationHandler.Verify)
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
				platform.GET("/tenants/:id/billing-calendar", (&handlers.BillingCalendarAdminHandler{Store: platformQueries}).Get)
				platform.PUT("/tenants/:id/billing-calendar", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), (&handlers.BillingCalendarAdminHandler{Store: platformQueries}).Put)
				platform.POST("/plans", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.UpsertPlan)
				platform.POST("/plan-prices", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.SetPlanPrice)
				platform.POST("/coupons", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.UpsertCoupon)
				platform.POST("/coupons/:code/redeem", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.RedeemCoupon)
				platform.GET("/brands/:id/settings", platformHandler.BrandSettings)
				platform.PATCH("/brands/:id/settings", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.UpdateBrandSettings)
				platform.GET("/brands", brandingHandler.List)
				platform.POST("/brands", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), brandingHandler.Save)
				platform.GET("/plans", platformHandler.Plans)
				platform.GET("/brand-hosts", brandingHandler.Hosts)
				platform.POST("/brand-hosts", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), brandingHandler.BindHost)
				platform.DELETE("/brand-hosts/:hostname", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), brandingHandler.UnbindHost)
				platform.PATCH("/brands/:id/status", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), brandingHandler.SetStatus)
				platform.DELETE("/brands/:id", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), brandingHandler.Delete)
				platform.POST("/brands/:id/reset-theme", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), brandingHandler.ResetTheme)
				platform.DELETE("/plans/:code", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), brandingHandler.DeletePlan)
				platform.GET("/coupon-reissues", (&handlers.CouponReissueHandler{Store: platformQueries}).List)
				platform.POST("/tenants/:id/coupon-reissues", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), (&handlers.CouponReissueHandler{Store: platformQueries}).Issue)
				platform.GET("/coupons", platformHandler.Coupons)
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
				platform.POST("/orders/:id/query", paymentGateway.QueryLegacy)
				platform.GET("/payment-reviews", (&handlers.PaymentReviewHandler{Pool: opts.PlatformPool}).List)
				platform.POST("/payment-reviews/:id/resolve", (&handlers.PaymentReviewHandler{Pool: opts.PlatformPool}).Resolve)
				platform.GET("/billing-document-profile", invoiceProfile.GetSeller)
				platform.PUT("/billing-document-profile", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), invoiceProfile.PutSeller)
				platform.POST("/refunds/:id/query", (&handlers.RefundQueryHandler{Service: &refund.Service{Pool: opts.PlatformPool, Registry: paymentGateway.Registry, LegacyStripe: opts.Payment}}).Query)
				platform.GET("/refunds", refundAdmin.List)
				platform.POST("/refunds/:id/approve", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), refundAdmin.Approve)
				platform.GET("/tenants/:id/ai/configuration", aiConfiguration.PlatformGet)
				platform.POST("/tenants/:id/ai/configuration", aiConfiguration.PlatformPut)
				platform.GET("/tenants/:id/ai/usage", aiConfiguration.PlatformUsage)
				platform.POST("/tenants/:id/ai/health", middleware.RateLimitWindow(5, time.Minute, middleware.TenantRateKey), aiConfiguration.PlatformHealth)
				platform.POST("/tenants/:id/ai/usage/:request_id/resolve", aiConfiguration.ResolveUsage)
				platform.PUT("/tenants/:id/ai", aiConfiguration.PlatformPut)
				platform.POST("/tenants/:id/ai/requests/:request_id/resolve", aiConfiguration.ResolveUsage)
				platform.GET("/reminder-policy", (&handlers.NotificationConfigHandler{Store: platformQueries, Secrets: opts.Secrets}).ReminderPolicy)
				platform.PUT("/reminder-policy", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), (&handlers.NotificationConfigHandler{Store: platformQueries, Secrets: opts.Secrets}).SaveReminderPolicy)
				platform.PUT("/notification-channel", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), (&handlers.NotificationConfigHandler{Store: platformQueries, Secrets: opts.Secrets}).Save)
				platform.POST("/channels", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.UpsertChannel)
				platform.POST("/payment-merchants/:merchant_id/orders/:order_id/query", paymentGateway.Query)
				platform.GET("/payment-merchants", paymentGateway.ListMerchants)
				platform.POST("/payment-merchants", paymentGateway.VerifyConfiguration, middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), paymentGateway.SaveMerchant)
				platform.PATCH("/payment-merchants/:merchant_id", paymentGateway.VerifyConfiguration, middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), paymentGateway.SaveMerchant)
				platform.POST("/tenants/:id/suspend", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.SuspendTenant)
				platform.POST("/tenants/:id/activate", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.ActivateTenant)
				platform.POST("/tenants/:id/close", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.CloseTenant)
				platform.PATCH("/tenants/:id", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.UpdateTenant)
				platform.POST("/tenants/:id/owner", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.TransferOwner)
				platform.POST("/tenants/:id/subscription", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.AssignSubscription)
				platform.POST("/tenants/:id/marketing/channel", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), marketingHandler.ConfigureChannel)
				platform.PATCH("/tenants/:id/marketing/channels/:channel_id", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), marketingHandler.ConfigureChannel)
				platform.POST("/tenants/:id/marketing/recipients/:recipient_id/resolve", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), marketingHandler.ResolveAttempt)
				platform.GET("/mail/queue", deliveryHandler.PlatformQueue)
				platform.GET("/mail/collectors", deliveryHandler.CollectorHealth)
				platform.POST("/tenants/:id/invoices", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.CreateInvoice)
				platform.POST("/invoices/:id/paid", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.MarkInvoicePaid)
				platform.POST("/orders/:id/paid", platformHandler.MarkOrderPaid)
				platform.POST("/invoices/:id/void", middleware.RequirePlatformTx(opts.PlatformPool, opts.Logger), platformHandler.VoidInvoice)
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

		protected := v1.Group("")
		if opts.Audit != nil {
			protected.Use(middleware.Audit(opts.Audit, opts.Logger))
		}
		protected.Use(middleware.RequireAuth(authQueries))
		protected.Use(middleware.RequireTenantTx(opts.AppPool, opts.Logger))
		protected.Use(middleware.RequireCSRF())
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
			protected.GET("/domains", domainHandler.List)
			protected.POST("/domains", middleware.RequireRole("owner", "admin"), domainHandler.Create)
			protected.POST("/domains/:id/verify", middleware.RequireRole("owner", "admin"), domainHandler.Verify)
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
			protected.GET("/marketing/campaigns/:campaign_id/preview", middleware.RequireRole("owner", "admin"), marketingHandler.PreviewCampaign)
			protected.GET("/notifications", notificationHandler.List)
			protected.POST("/ai/consent", middleware.RequireRole("owner", "admin"), middleware.RateLimitWindow(10, time.Minute, middleware.TenantRateKey), aiConfiguration.Consent)
			protected.GET("/ai/configuration", middleware.RequireRole("owner", "admin"), aiConfiguration.Get)
			protected.PUT("/ai/configuration", middleware.RequireRole("owner", "admin"), aiConfiguration.Put)
			protected.GET("/ai/usage", middleware.RequireRole("owner", "admin"), aiConfiguration.Usage)
			protected.POST("/ai/health", middleware.RequireRole("owner", "admin"), middleware.RateLimitWindow(5, time.Minute, middleware.TenantRateKey), aiConfiguration.Health)
			protected.GET("/marketing/channels", middleware.RequireRole("owner", "admin"), marketingHandler.Channels)
			protected.GET("/marketing/templates", middleware.RequireRole("owner", "admin"), marketingHandler.Templates)
			protected.POST("/marketing/templates", middleware.RequireRole("owner", "admin"), marketingHandler.CreateTemplate)
			protected.GET("/marketing/lists", middleware.RequireRole("owner", "admin"), marketingHandler.Lists)
			protected.POST("/marketing/lists", middleware.RequireRole("owner", "admin"), marketingHandler.CreateList)
			protected.GET("/marketing/lists/:list_id/contacts", middleware.RequireRole("owner", "admin"), marketingHandler.Contacts)
			protected.POST("/marketing/lists/:list_id/contacts", middleware.RequireRole("owner", "admin"), marketingHandler.ImportContacts)
			protected.GET("/marketing/campaigns", middleware.RequireRole("owner", "admin"), marketingHandler.Campaigns)
			protected.POST("/marketing/campaigns", middleware.RequireRole("owner", "admin"), marketingHandler.CreateCampaign)
			protected.POST("/marketing/campaigns/:campaign_id/confirm", middleware.RequireRole("owner", "admin"), marketingHandler.ConfirmCampaign)
			protected.PATCH("/marketing/campaigns/:campaign_id", middleware.RequireRole("owner", "admin"), marketingHandler.ChangeCampaign)
			protected.GET("/marketing/campaigns/:campaign_id/recipients", middleware.RequireRole("owner", "admin"), marketingHandler.Recipients)
			protected.GET("/marketing/suppressions", middleware.RequireRole("owner", "admin"), marketingSuppressionHandler.List)
			protected.POST("/marketing/suppressions", middleware.RequireRole("owner", "admin"), marketingSuppressionHandler.Add)
			protected.DELETE("/marketing/suppressions", middleware.RequireRole("owner", "admin"), marketingSuppressionHandler.Remove)

			// Billing and distribution (tenant-scoped reads)
			protected.GET("/billing/document-profile", middleware.RequireRole("owner", "admin", "billing"), invoiceProfile.GetBuyer)
			protected.PUT("/billing/document-profile", middleware.RequireRole("owner", "admin", "billing"), invoiceProfile.PutBuyer)
			protected.GET("/billing/plans", billingHandler.Plans)
			protected.GET("/billing/payment-methods", paymentGateway.Methods)

			// Quote and checkout are rate limited per tenant: without a budget a
			// tenant could flood pending orders and, with a reserved coupon,
			// burn global quota (M5 review 3.27).
			protected.POST("/billing/quote", middleware.RequireRole("owner", "admin", "billing"),
				middleware.RateLimitWindow(30, time.Minute, middleware.TenantRateKey), billingHandler.Quote)
			protected.POST("/billing/checkout", middleware.RequireRole("owner", "admin", "billing"),
				middleware.RateLimitWindow(20, time.Minute, middleware.TenantRateKey), billingHandler.Checkout)
			protected.POST("/billing/orders/:id/cancel", middleware.RequireRole("owner", "admin", "billing"), billingHandler.CancelOrder)
			protected.POST("/billing/refunds", middleware.RequireRole("owner", "admin", "billing"), billingHandler.CreateRefundRequest)
			protected.GET("/billing/refunds", middleware.RequireRole("owner", "admin", "billing"), billingHandler.ListRefundRequests)
			protected.GET("/billing/orders", middleware.RequireRole("owner", "admin", "billing"), billingHandler.Orders)
			protected.GET("/billing/invoices/:id/pdf", middleware.RequireRole("owner", "admin", "billing"), billingHandler.InvoicePDF)
			protected.GET("/billing/subscription", billingHandler.Subscription)
			protected.POST("/billing/subscription/change", middleware.RequireRole("owner", "admin", "billing"), billingHandler.ScheduleChange)
			protected.POST("/billing/subscription/change/cancel", middleware.RequireRole("owner", "admin", "billing"), billingHandler.CancelScheduledChange)
			protected.POST("/billing/subscription/cancel", middleware.RequireRole("owner", "admin", "billing"), billingHandler.CancelSubscription)
			protected.GET("/billing/invoices", middleware.RequireRole("owner", "admin", "billing"), billingHandler.Invoices)
			protected.GET("/billing/distribution", middleware.RequireRole("owner", "admin", "billing"), billingHandler.Distribution)
			protected.POST("/billing/coupons/validate", middleware.RequireRole("owner", "admin", "billing"), billingHandler.ValidateCoupon)
			protected.POST("/billing/trial", middleware.RequireRole("owner", "admin", "billing"), middleware.RateLimitWindow(10, time.Minute, middleware.TenantRateKey), billingHandler.RedeemTrial)

			// Archive / legal hold
			protected.GET("/archive", archiveHandler.List)
			protected.GET("/archive/search", middleware.RequireRole("owner", "admin"), archiveContent.Search)
			protected.POST("/archive/:id/restore", middleware.RequireRole("owner", "admin"), archiveContent.Restore)
			protected.GET("/archive/:id/attachments/:attachment", middleware.RequireRole("owner", "admin"), archiveContent.Attachment)
			protected.GET("/archive/:id/content", middleware.RequireRole("owner", "admin"), archiveContent.Content)
			protected.GET("/archive/:id/export", middleware.RequireRole("owner", "admin"), archiveContent.Export)
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
			protected.GET("/support/help", supportHandler.Help)

			protected.POST("/compliance/recalls", middleware.RequireRole("owner", "admin"), recallHandler.Create)
			protected.GET("/compliance/recalls", middleware.RequireRole("owner", "admin"), recallHandler.List)
			protected.GET("/compliance/recalls/:id", middleware.RequireRole("owner", "admin"), recallHandler.Get)
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

		status := gin.H{"status": "ok", "version": opts.Version, "mode": "production", "quarantined": opts.Quarantined}
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

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
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/dnsprovider"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/web"
)

// Options carries the dependencies of the HTTP server.
type Options struct {
	DevMode bool
	Logger  *zap.Logger
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
	Recall handlers.RecallExpunger
	// Audit receives one row per successful mutating tenant request.
	Audit middleware.AuditWriter
	// Secrets encrypts TOTP seeds at rest. Nil keeps legacy plaintext support.
	Secrets handlers.SecretsBox
	// DNSProvider writes verification records for delegated domains when set.
	DNSProvider dnsprovider.Provider
	// Payment enables Stripe Checkout and webhook settlement when set.
	Payment *payment.Stripe
	// BaseURL is the public panel origin used for Stripe redirects.
	BaseURL string
}

// New builds the gin engine with all middleware and routes attached.
func New(opts Options) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	engine := gin.New()
	// The reverse proxy is the only trusted peer. Client-supplied
	// X-Forwarded-For headers must not influence rate limiting or audit IPs.
	_ = engine.SetTrustedProxies([]string{"127.0.0.1", "::1"})
	engine.Use(middleware.RequestID())
	engine.Use(middleware.Recover(opts.Logger))
	engine.Use(middleware.AccessLog(opts.Logger))
	engine.Use(middleware.BodyLimit(1 << 20))
	engine.Use(middleware.Timeout(30 * time.Second))

	engine.GET("/metrics", gin.WrapH(promhttp.Handler()))

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
	domainHandler := handlers.NewDomainHandler(appQueries, opts.Reconcile)
	domainHandler.DNS = opts.DNSProvider
	domainHandler.Hostname = opts.MyHostname
	mailboxHandler := handlers.NewMailboxHandler(appQueries, opts.Reconcile)
	aliasHandler := handlers.NewAliasHandler(appQueries, opts.Reconcile)
	archiveHandler := handlers.NewArchiveHandler(appQueries)
	recallHandler := &handlers.RecallHandler{Store: appQueries, Platform: platformQueries, Expunger: opts.Recall}
	auditHandler := handlers.NewAuditHandler(appQueries)
	billingHandler := handlers.NewBillingHandler(appQueries)
	billingHandler.Pool = opts.PlatformPool
	billingHandler.Stripe = opts.Payment
	billingHandler.BaseURL = opts.BaseURL
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

	v1 := engine.Group("/api/v1")
	{
		v1.GET("/healthz", healthz(opts))
		v1.GET("/branding", brandingHandler.Public)
		v1.GET("/i18n/:locale", i18nCatalog(opts))

		v1.POST("/billing/webhook/stripe", billingHandler.StripeWebhook)

		// Public Auth
		authGroup := v1.Group("/auth")
		{
			authGroup.POST("/login", authHandler.Login)
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
			platform := v1.Group("/platform")
			platform.Use(middleware.RequireAuth(authQueries))
			platform.Use(middleware.RequirePlatformAdmin(authQueries))
			platform.Use(middleware.RequireCSRF())
			platform.Use(middleware.AuditPlatform(opts.Audit, opts.Logger))
			{
				platform.GET("/overview", platformHandler.Overview)
				platform.GET("/settings", platformHandler.Settings)
				platform.PATCH("/settings", platformHandler.UpdateSettings)
				platform.GET("/tenants", platformHandler.ListTenants)
				platform.POST("/tenants", platformHandler.CreateTenant)
				platform.POST("/plans", platformHandler.UpsertPlan)
				platform.POST("/coupons", platformHandler.UpsertCoupon)
				platform.POST("/coupons/:code/redeem", platformHandler.RedeemCoupon)
				platform.GET("/brands/:id/settings", platformHandler.BrandSettings)
				platform.PATCH("/brands/:id/settings", platformHandler.UpdateBrandSettings)
				platform.GET("/brands", brandingHandler.List)
				platform.POST("/brands", brandingHandler.Save)
				platform.GET("/plans", platformHandler.Plans)
				platform.GET("/brand-hosts", brandingHandler.Hosts)
				platform.POST("/brand-hosts", brandingHandler.BindHost)
				platform.DELETE("/brand-hosts/:hostname", brandingHandler.UnbindHost)
				platform.PATCH("/brands/:id/status", brandingHandler.SetStatus)
				platform.DELETE("/brands/:id", brandingHandler.Delete)
				platform.POST("/brands/:id/reset-theme", brandingHandler.ResetTheme)
				platform.DELETE("/plans/:code", brandingHandler.DeletePlan)
				platform.GET("/coupons", platformHandler.Coupons)
				platform.GET("/channels", platformHandler.Channels)
				platform.GET("/audit", platformHandler.AuditLogs)
				platform.POST("/channels", platformHandler.UpsertChannel)
				platform.POST("/tenants/:id/suspend", platformHandler.SuspendTenant)
				platform.POST("/tenants/:id/activate", platformHandler.ActivateTenant)
				platform.POST("/tenants/:id/close", platformHandler.CloseTenant)
				platform.PATCH("/tenants/:id", platformHandler.UpdateTenant)
				platform.POST("/tenants/:id/owner", platformHandler.TransferOwner)
				platform.POST("/tenants/:id/subscription", platformHandler.AssignSubscription)
				platform.POST("/tenants/:id/invoices", platformHandler.CreateInvoice)
				platform.POST("/invoices/:id/paid", platformHandler.MarkInvoicePaid)
				platform.POST("/orders/:id/paid", platformHandler.MarkOrderPaid)
				platform.POST("/invoices/:id/void", platformHandler.VoidInvoice)
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
		protected := v1.Group("")
		protected.Use(middleware.RequireAuth(authQueries))
		protected.Use(middleware.RequireTenantTx(opts.AppPool, opts.Logger))
		protected.Use(middleware.RequireCSRF())
		if opts.Audit != nil {
			protected.Use(middleware.Audit(opts.Audit, opts.Logger))
		}
		{
			// Tenants
			protected.GET("/tenants/current", tenantHandler.Current)
			protected.PATCH("/tenants/current", middleware.RequireRole("owner", "admin"), tenantHandler.UpdateSettings)

			// Domains
			protected.GET("/domains", domainHandler.List)
			protected.POST("/domains", middleware.RequireRole("owner", "admin"), domainHandler.Create)
			protected.POST("/domains/:id/verify", middleware.RequireRole("owner", "admin"), domainHandler.Verify)
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

			// Billing and distribution (tenant-scoped reads)
			protected.GET("/billing/plans", billingHandler.Plans)
			protected.POST("/billing/checkout", middleware.RequireRole("owner", "admin"), billingHandler.Checkout)
			protected.GET("/billing/orders", billingHandler.Orders)
			protected.GET("/billing/invoices/:id/pdf", billingHandler.InvoicePDF)
			protected.GET("/billing/subscription", billingHandler.Subscription)
			protected.GET("/billing/invoices", billingHandler.Invoices)
			protected.GET("/billing/distribution", billingHandler.Distribution)
			protected.POST("/billing/coupons/validate", billingHandler.ValidateCoupon)

			// Archive / legal hold
			protected.GET("/archive", archiveHandler.List)
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

		status := gin.H{"status": "ok", "version": opts.Version, "mode": "production"}
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

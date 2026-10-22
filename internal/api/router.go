// Package api wires the HTTP surface of urMail.cn.
package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/handlers"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/web"
)

// Options carries the dependencies of the HTTP server.
type Options struct {
	Logger *zap.Logger
	// AppPool is the tenant-scoped pool.
	AppPool *db.Pool
	// PlatformPool is the platform-scoped pool.
	PlatformPool *db.Pool
	// Messages is the message catalogue.
	Messages *i18n.Store
	// Version is reported by /api/v1/healthz.
	Version string
	// Reconcile is the configuration reconcile trigger.
	Reconcile handlers.ReconcileTrigger
}

// New builds the gin engine with all middleware and routes attached.
func New(opts Options) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	engine := gin.New()
	engine.Use(middleware.RequestID())
	engine.Use(middleware.Recover(opts.Logger))
	engine.Use(middleware.AccessLog(opts.Logger))
	engine.Use(middleware.Timeout(30 * time.Second))

	engine.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// Bundled web assets
	engine.GET("/assets/*filepath", gin.WrapH(web.AssetHandler()))

	var appQueries *store.Queries
	if opts.AppPool != nil {
		appQueries = store.New(opts.AppPool.Pool)
	}

	authHandler := handlers.NewAuthHandler(appQueries)
	tenantHandler := handlers.NewTenantHandler(appQueries)
	domainHandler := handlers.NewDomainHandler(appQueries, opts.Reconcile)
	mailboxHandler := handlers.NewMailboxHandler(appQueries, opts.Reconcile)
	aliasHandler := handlers.NewAliasHandler(appQueries, opts.Reconcile)

	v1 := engine.Group("/api/v1")
	{
		v1.GET("/healthz", healthz(opts))
		v1.GET("/i18n/:locale", i18nCatalogue(opts))

		// Public Auth
		authGroup := v1.Group("/auth")
		{
			authGroup.POST("/login", authHandler.Login)
		}

		// Protected Routes
		protected := v1.Group("")
		protected.Use(middleware.RequireAuth(appQueries))
		protected.Use(middleware.RequireCSRF())
		{
			// Auth
			protected.POST("/auth/logout", authHandler.Logout)
			protected.GET("/auth/me", authHandler.Me)

			// Tenants
			protected.GET("/tenants/current", tenantHandler.Current)

			// Domains
			protected.GET("/domains", domainHandler.List)
			protected.POST("/domains", domainHandler.Create)
			protected.DELETE("/domains/:id", domainHandler.Delete)

			// Mailboxes
			protected.GET("/mailboxes", mailboxHandler.List)
			protected.POST("/mailboxes", mailboxHandler.Create)
			protected.POST("/mailboxes/:id/password", mailboxHandler.UpdatePassword)
			protected.POST("/mailboxes/:id/quota", mailboxHandler.UpdateQuota)
			protected.DELETE("/mailboxes/:id", mailboxHandler.Delete)

			// Aliases
			protected.GET("/aliases", aliasHandler.List)
			protected.POST("/aliases", aliasHandler.Create)
			protected.DELETE("/aliases/:id", aliasHandler.Delete)
		}
	}

	// Single-Page Application Fallback
	engine.NoRoute(func(c *gin.Context) {
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
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

		status := gin.H{"status": "ok", "version": opts.Version}
		if err := ping(ctx, opts.AppPool); err != nil {
			status["status"] = "degraded"
			status["db"] = err.Error()
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

func i18nCatalogue(opts Options) gin.HandlerFunc {
	return func(c *gin.Context) {
		locale := c.Param("locale")
		if locale == "" {
			locale = i18n.DefaultLocale
		}
		c.JSON(http.StatusOK, opts.Messages.Raw(locale))
	}
}

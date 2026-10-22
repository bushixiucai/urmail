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

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/i18n"
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

	// The bundled assets are served with their real content type. Everything
	// else falls through to the single-page application.
	engine.GET("/assets/*filepath", gin.WrapH(web.AssetHandler()))

	v1 := engine.Group("/api/v1")
	{
		v1.GET("/healthz", healthz(opts))
		v1.GET("/i18n/:locale", i18nCatalogue(opts))
	}

	// The SPA is served from the same origin as the API. Unknown paths fall
	// through to index.html so that client-side routes survive a reload.
	engine.NoRoute(func(c *gin.Context) {
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND"}})
			return
		}
		// A missing asset is a missing asset, not a client-side route.
		if web.IsAssetPath(c.Request.URL.Path) {
			c.Status(http.StatusNotFound)
			return
		}
		web.ServeIndex(c)
	})

	return engine
}

// healthz reports whether the process can serve requests. A database failure
// makes the box unhealthy: the management plane is useless without it.
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

// ping checks the database, tolerating a nil pool so that the server can
// start before the database is reachable.
func ping(ctx context.Context, pool *db.Pool) error {
	if pool == nil {
		return errors.New("database pool is not configured")
	}
	if err := pool.Ping(ctx); err != nil {
		return err
	}
	return nil
}

// i18nCatalogue serves the message catalogue of a locale.
//
// The catalogue is served from the same embedded copy the backend uses, so the
// frontend and the backend can never disagree about a message.
func i18nCatalogue(opts Options) gin.HandlerFunc {
	return func(c *gin.Context) {
		locale := c.Param("locale")
		if locale == "" {
			locale = i18n.DefaultLocale
		}
		c.JSON(http.StatusOK, opts.Messages.Raw(locale))
	}
}

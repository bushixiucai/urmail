package middleware

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// ContextBrandID scopes a request made through an active brand host.
const ContextBrandID = "urmail.brand_id"

const canonicalOriginKey = "urmail.canonical_origin"

// OriginHost retains the port because browser origins distinguish it.
func OriginHost(raw string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
}

// BrandHostBoundary rejects stale routes immediately after revocation, and
// scopes active branded requests before authentication or public handlers run.
func BrandHostBoundary(pool *db.Pool, canonical ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := OriginHost(c.Request.Host)
		if len(canonical) > 0 && canonical[0] != "" {
			c.Set(canonicalOriginKey, OriginHost(canonical[0]))
		}
		c.Request = c.Request.WithContext(webmail.WithOriginHost(c.Request.Context(), origin))
		if pool == nil {
			c.Next()
			return
		}
		host := origin
		if value, _, e := net.SplitHostPort(host); e == nil {
			host = value
		}
		host = strings.TrimSuffix(host, ".")
		row, err := store.New(pool).PlatformGetBrandHostLifecycle(c, host)
		if errors.Is(err, pgx.ErrNoRows) {
			c.Next()
			return
		}
		if err != nil {
			c.AbortWithStatusJSON(503, gin.H{"error": gin.H{"code": "DB_ERROR", "message": "error.DB_ERROR"}})
			return
		}
		if row.Status != "active" {
			c.AbortWithStatusJSON(http.StatusMisdirectedRequest, gin.H{"error": gin.H{"code": "BRAND_HOST_INACTIVE", "message": "error.BRAND_HOST_INACTIVE"}})
			return
		}
		eligible, err := store.New(pool).PlatformBrandHostCertEligible(c, host)
		if err != nil || !eligible {
			c.AbortWithStatus(http.StatusMisdirectedRequest)
			return
		}
		c.Set(ContextBrandID, row.BrandID)
		c.Next()
	}
}

// BrandTenantAllowed is checked after current session authentication.
func BrandTenantAllowed(ctx context.Context, pool *db.Pool, tenant, brand int64) bool {
	if pool == nil {
		return false
	}
	allowed, err := store.New(pool).PlatformTenantBrandMatches(ctx, store.PlatformTenantBrandMatchesParams{ID: tenant, BrandID: brand})
	return err == nil && allowed
}

// legacyOriginAllowed limits pre-migration unbound tokens to the configured
// canonical endpoint. Empty configuration is only embedded-router compatibility.
func legacyOriginAllowed(c *gin.Context) bool {
	value, configured := c.Get(canonicalOriginKey)
	if !configured {
		return true
	}
	canonical, ok := value.(string)
	if !ok || canonical == "" {
		return false
	}
	actual := OriginHost(c.Request.Host)
	if _, _, err := net.SplitHostPort(canonical); err == nil {
		return actual == canonical
	}
	if host, port, err := net.SplitHostPort(actual); err == nil {
		if port != "443" && port != "80" {
			return false
		}
		actual = host
	}
	return strings.TrimSuffix(actual, ".") == canonical
}

func sessionOriginAllowed(c *gin.Context, origin string) bool {
	if origin != "" {
		return origin == OriginHost(c.Request.Host)
	}
	if _, branded := c.Get(ContextBrandID); branded {
		return false
	}
	return legacyOriginAllowed(c)
}

// CanonicalHostAllowed keeps global operator endpoints on the configured
// canonical host; custom brand hosts never inherit platform administration.
func CanonicalHostAllowed(c *gin.Context) bool {
	if _, branded := c.Get(ContextBrandID); branded {
		return false
	}
	return legacyOriginAllowed(c)
}

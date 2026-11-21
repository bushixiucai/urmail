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

// BrandHostLookup is the storage seam behind the brand boundary: one
// lifecycle lookup plus TLS eligibility per branded host. The PostgreSQL
// pool adapter and the SQLite slice-1 store both implement it.
type BrandHostLookup interface {
	// BrandHostLifecycle reports found=false for hosts that are not brand
	// hosts at all (the boundary passes them through).
	BrandHostLifecycle(ctx context.Context, host string) (brandID int64, status string, found bool, err error)
	BrandHostCertEligible(ctx context.Context, host string) (eligible bool, err error)
}

// brandHostBoundary is the shared core: stale routes are rejected
// immediately after revocation and active branded requests are scoped
// before authentication or public handlers run.
func brandHostBoundary(lookup BrandHostLookup, canonical ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := OriginHost(c.Request.Host)
		if len(canonical) > 0 && canonical[0] != "" {
			c.Set(canonicalOriginKey, OriginHost(canonical[0]))
		}
		c.Request = c.Request.WithContext(webmail.WithOriginHost(c.Request.Context(), origin))
		if lookup == nil {
			c.Next()
			return
		}
		host := origin
		if value, _, e := net.SplitHostPort(host); e == nil {
			host = value
		}
		host = strings.TrimSuffix(host, ".")
		brandID, status, found, err := lookup.BrandHostLifecycle(c.Request.Context(), host)
		// Error FIRST: a lookup/scan failure must fail closed with the
		// fixed 503 DB_ERROR body (no engine detail leaks). Only an explicit
		// "no such brand host" (err == nil, found == false) passes through;
		// found=false with err≠nil never skips the boundary.
		if err != nil {
			c.AbortWithStatusJSON(503, gin.H{"error": gin.H{"code": "DB_ERROR", "message": "error.DB_ERROR"}})
			return
		}
		if !found {
			c.Next()
			return
		}
		if status != "active" {
			c.AbortWithStatusJSON(http.StatusMisdirectedRequest, gin.H{"error": gin.H{"code": "BRAND_HOST_INACTIVE", "message": "error.BRAND_HOST_INACTIVE"}})
			return
		}
		eligible, err := lookup.BrandHostCertEligible(c.Request.Context(), host)
		if err != nil || !eligible {
			c.AbortWithStatus(http.StatusMisdirectedRequest)
			return
		}
		c.Set(ContextBrandID, brandID)
		c.Next()
	}
}

// BrandHostBoundaryLookup scopes requests through an explicit storage
// lookup — the SQLite slice-1 runtime wires its store here; the semantics
// are the shared core above.
func BrandHostBoundaryLookup(lookup BrandHostLookup, canonical ...string) gin.HandlerFunc {
	return brandHostBoundary(lookup, canonical...)
}

// BrandHostBoundary scopes requests through the PostgreSQL pool. Behavior
// (including the nil-pool pass-through) is identical to the lookup-based
// core; the pool adapter resolves the two brand queries per request.
func BrandHostBoundary(pool *db.Pool, canonical ...string) gin.HandlerFunc {
	if pool == nil {
		return brandHostBoundary(nil, canonical...)
	}
	return brandHostBoundary(pgBrandHostLookup{pool: pool}, canonical...)
}

type pgBrandHostLookup struct{ pool *db.Pool }

func (p pgBrandHostLookup) BrandHostLifecycle(ctx context.Context, host string) (int64, string, bool, error) {
	row, err := db.Queries(p.pool).PlatformGetBrandHostLifecycle(ctx, host)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	return row.BrandID, row.Status, true, nil
}

func (p pgBrandHostLookup) BrandHostCertEligible(ctx context.Context, host string) (bool, error) {
	return db.Queries(p.pool).PlatformBrandHostCertEligible(ctx, host)
}

// BrandTenantAllowed is checked after current session authentication.
func BrandTenantAllowed(ctx context.Context, pool *db.Pool, tenant, brand int64) bool {
	if pool == nil {
		return false
	}
	allowed, err := db.Queries(pool).PlatformTenantBrandMatches(ctx, store.PlatformTenantBrandMatchesParams{ID: tenant, BrandID: brand})
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

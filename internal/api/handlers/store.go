package handlers

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
)

// requestStore returns the request-scoped queries installed by the
// RequireTenantTx middleware when they satisfy the handler's store
// interface, falling back to the store the handler was constructed with.
//
// The fallback keeps unit tests (which use fake stores and no database)
// working; in production the middleware always installs a live store bound
// to the tenant transaction, so the row-level security context applies.
func requestStore[T any](c *gin.Context, fallback T) T {
	if v, ok := middleware.QueriesFromCtx(c); ok {
		if q, ok := v.(T); ok {
			return q
		}
	}
	return fallback
}

// parseTimeParam accepts an RFC 3339 timestamp or a date for the audit list
// filters; an unparsable value leaves the filter unset rather than failing
// the whole listing.
func parseTimeParam(raw string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), nil
	}
	t, err := time.ParseInLocation("2006-01-02", raw, time.UTC)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}

package handlers

import (
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/db/dberr"
)

// writeDatabaseError is the single mapping helper from the S7 frozen rule:
// constraints -> 409 (the caller's stale code), missing foreign keys -> 404,
// permission denials -> 403, genuinely unavailable databases -> 503, and
// everything else -> 500 with the raw driver error logged. Never widen a
// catch-all to "unavailable" to make a test green.
func writeDatabaseError(c *gin.Context, err error, conflictCode string) {
	switch dberr.Classify(err) {
	case dberr.Constraint:
		authError(c, 409, conflictCode)
	case dberr.ForeignKey:
		authError(c, 404, "NOT_FOUND")
	case dberr.Forbidden:
		authError(c, 403, "FORBIDDEN")
	case dberr.Unavailable:
		authError(c, 503, "ARCHIVE_UNAVAILABLE")
	default:
		zap.L().Error("database statement failed",
			zap.String("method", c.Request.Method),
			zap.String("route", c.FullPath()),
			zap.Error(err),
		)
		authError(c, 500, "DB_ERROR")
	}
}

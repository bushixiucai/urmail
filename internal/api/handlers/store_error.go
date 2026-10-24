package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
)

// respondConstraintError preserves safe business codes without exposing SQL data.
func respondConstraintError(c *gin.Context, err error) bool {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return false
	}
	switch pg.Message {
	case "DOMAIN_NOT_PROVISIONED", "MAILBOX_STORAGE_ADDRESS_RESERVED", "MAILBOX_STORAGE_IDENTITY_IMMUTABLE", "INVALID_MAILBOX_IDENTITY", "LEGAL_HOLD", "LEGAL_HOLD_DELETED", "STORAGE_QUOTA_EXCEEDED", "STORAGE_USAGE_UNKNOWN", "STORAGE_BELOW_USAGE", "STORAGE_ALLOCATION_REVIEW_REQUIRED", "STORAGE_NOT_EMPTY", "SUBSCRIPTION_REQUIRED", "SUBSCRIPTION_EXPIRED", "SUBSCRIPTION_PAST_DUE", "DEFAULT_PLAN_UNAVAILABLE", "INVALID_DEFAULT_QUOTA", "DOMAIN_QUOTA_EXCEEDED", "MAILBOX_LIMIT_EXCEEDED":
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": pg.Message, "message": "error." + pg.Message}})
		return true
	}
	return false
}

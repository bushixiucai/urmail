package handlers

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/refund"
)

// RefundQueryHandler imports verified provider facts without requesting money.
type RefundQueryHandler struct{ Service *refund.Service }

// Query reconciles observed provider facts without dispatching a refund.
func (h *RefundQueryHandler) Query(c *gin.Context) {
	actor, ok := middleware.GetUserID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	var body struct {
		Reason          string `json:"reason"`
		ConfirmObserved bool   `json:"confirm_observed"`
	}
	if c.ShouldBindJSON(&body) != nil || len(strings.TrimSpace(body.Reason)) < 10 || len(body.Reason) > 1000 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if h.Service == nil {
		authError(c, 503, "PAYMENT_PROVIDER_DISABLED")
		return
	}
	row, err := h.Service.ReconcileOne(c.Request.Context(), id, actor, body.Reason, body.ConfirmObserved)
	if err != nil {
		authError(c, 409, "REFUND_RECONCILIATION_REQUIRED")
		return
	}
	c.JSON(200, gin.H{"refund": row, "provider_query_only": true, "entitlements_applied": false})
}

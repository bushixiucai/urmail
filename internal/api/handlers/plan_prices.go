package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/store"
)

// SetPlanPrice updates one independently configured currency and sales cycle.
func (h *PlatformHandler) SetPlanPrice(c *gin.Context) {
	var input struct {
		PlanCode     string `json:"plan_code" binding:"required"`
		Currency     string `json:"currency" binding:"required"`
		BillingCycle string `json:"billing_cycle" binding:"required"`
		AmountCents  int64  `json:"amount_cents"`
	}
	if c.ShouldBindJSON(&input) != nil || input.AmountCents < 0 || input.AmountCents > 9007199254740991 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	input.Currency = strings.ToUpper(input.Currency)
	if (input.Currency != "CNY" && input.Currency != "USD") || (input.BillingCycle != billing.CycleMonthly && input.BillingCycle != billing.CycleQuarterly && input.BillingCycle != billing.CycleAnnual) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	queries, ok := requestStore(c, h.Store).(*store.Queries)
	if !ok {
		authError(c, 503, "DB_ERROR")
		return
	}
	price, err := queries.PlatformSetPlanPrice(c, store.PlatformSetPlanPriceParams{PlanCode: input.PlanCode, Currency: input.Currency, BillingCycle: input.BillingCycle, AmountCents: input.AmountCents})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		authError(c, 500, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, gin.H{"price": price})
}

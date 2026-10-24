package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/store"
)

// PublicPricingHandler exposes only catalog fields intended for prospective customers.
type PublicPricingHandler struct{ Store *store.Queries }

// Plans lists active purchasable offers with independently configured cycle prices.
func (h *PublicPricingHandler) Plans(c *gin.Context) {
	if h.Store == nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	plans, err := h.Store.ListActivePlans(c)
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	offers := make([]gin.H, 0, len(plans))
	for _, plan := range plans {
		if !purchasablePlan(plan) {
			continue
		}
		prices, err := h.Store.ListPlanPrices(c, plan.Code)
		if err != nil {
			authError(c, 500, "DB_ERROR")
			return
		}
		// Legacy monthly catalog entries remain explicit monthly prices; no other cycle is derived.
		if len(prices) == 0 {
			prices = append(prices, store.ListPlanPricesRow{Currency: plan.Currency, BillingCycle: "monthly", AmountCents: plan.PriceCents})
		}
		offers = append(offers, gin.H{"code": plan.Code, "name": json.RawMessage(plan.Name), "description": json.RawMessage(plan.Description), "mailbox_limit": plan.MailboxLimit, "domain_limit": plan.DomainLimit, "quota_bytes": plan.QuotaBytes, "daily_send_limit": plan.DailySendLimit, "prices": prices})
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"plans": offers})
}

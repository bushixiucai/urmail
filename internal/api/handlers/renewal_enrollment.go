package handlers

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/billingrenew"
)

// RegisterRenewalRoutes adds explicit consent independently of subscription
// cancellation. Tenant requests can select only server-verified binding IDs.
func RegisterRenewalRoutes(group *gin.RouterGroup, service billingrenew.EnrollmentService) {
	h := renewalHandler{service: service}
	routes := group.Group("/billing/auto-renewal", middleware.RequireRole("owner", "admin", "billing"))
	routes.GET("", h.overview)
	routes.POST("/enroll", middleware.RateLimitWindow(10, time.Minute, middleware.TenantRateKey), h.enroll)
	routes.POST("/cancel", h.cancel)
}

// RegisterRecurringAdminRoutes belongs to the existing platform-admin group.
// These endpoints verify provider evidence; they cannot manufacture approval.
func RegisterRecurringAdminRoutes(group *gin.RouterGroup, dispatcher *billingrenew.Dispatcher) {
	group.POST("/payment-merchants/:merchant_id/recurring/verify", func(c *gin.Context) {
		id, err := strconv.ParseInt(c.Param("merchant_id"), 10, 64)
		var body struct {
			AdapterKey string `json:"adapter_key"`
		}
		if err != nil || id <= 0 || c.ShouldBindJSON(&body) != nil {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		if dispatcher == nil {
			authError(c, 409, "ADAPTER_UNAVAILABLE")
			return
		}
		ctx, cancel := boundedProviderContext(c)
		defer cancel()
		if err = dispatcher.ApproveMerchant(ctx, id, body.AdapterKey, recurringPlatformActor(c)); err != nil {
			authError(c, 409, "CAPABILITY_UNVERIFIED")
			return
		}
		c.JSON(200, gin.H{"verified": true})
	})
	group.POST("/tenants/:id/recurring-authorizations/verify", func(c *gin.Context) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		var body struct {
			SubscriptionID    int64  `json:"subscription_id"`
			MerchantID        int64  `json:"merchant_id"`
			ProviderReference string `json:"provider_reference"`
		}
		if err != nil || id <= 0 || c.ShouldBindJSON(&body) != nil {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		if dispatcher == nil {
			authError(c, 409, "ADAPTER_UNAVAILABLE")
			return
		}
		ctx, cancel := boundedProviderContext(c)
		defer cancel()
		binding, err := dispatcher.VerifyBinding(ctx, id, body.SubscriptionID, body.MerchantID, body.ProviderReference, recurringPlatformActor(c))
		if err != nil {
			authError(c, 409, "AUTHORIZATION_REQUIRED")
			return
		}
		c.JSON(200, gin.H{"binding_id": binding})
	})
}

type renewalHandler struct {
	service billingrenew.EnrollmentService
}

func renewalHTTPError(c *gin.Context, err error) {
	var rule billingrenew.RuleError
	if errors.As(err, &rule) {
		authError(c, 409, string(rule))
		return
	}
	if errors.Is(err, billingrenew.ErrNoChargingChannel) {
		authError(c, 409, "ADAPTER_UNAVAILABLE")
		return
	}
	authError(c, 503, "DB_ERROR")
}
func (h renewalHandler) overview(c *gin.Context) {
	tenant, ok := middleware.GetTenantID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	out, err := h.service.Overview(c.Request.Context(), tenant)
	if err != nil {
		renewalHTTPError(c, err)
		return
	}
	c.JSON(200, out)
}
func (h renewalHandler) enroll(c *gin.Context) {
	tenant, tok := middleware.GetTenantID(c)
	user, uok := middleware.GetUserID(c)
	if !tok || !uok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	var body struct {
		BindingID      int64  `json:"binding_id"`
		ConsentVersion string `json:"consent_version"`
		QuoteDigest    string `json:"quote_digest"`
		Confirmed      bool   `json:"confirmed"`
	}
	if c.ShouldBindJSON(&body) != nil || body.BindingID <= 0 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	middleware.AfterCommit(c, func() {
		e, err := h.service.Enroll(c.Request.Context(), tenant, user, body.BindingID, body.ConsentVersion, body.QuoteDigest, body.Confirmed)
		if err != nil {
			renewalHTTPError(c, err)
			return
		}
		c.JSON(200, gin.H{"enrollment": e})
	})
}
func (h renewalHandler) cancel(c *gin.Context) {
	tenant, ok := middleware.GetTenantID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	user, _ := middleware.GetUserID(c)
	middleware.AfterCommit(c, func() {
		e, flight, err := h.service.Cancel(c.Request.Context(), tenant, user)
		if err != nil {
			renewalHTTPError(c, err)
			return
		}
		c.JSON(200, gin.H{"enrollment": e, "in_flight": flight})
	})
}

func boundedProviderContext(c *gin.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), 30*time.Second)
}

// recurringPlatformActor never falls back to the trusted in-process variant.
func recurringPlatformActor(c *gin.Context) int64 {
	actor, ok := middleware.GetUserID(c)
	if !ok {
		return 0
	}
	return actor
}

package handlers

import (
	"errors"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/billingrenew"
)

// RegisterHostedRenewalRoutes belongs to the authenticated tenant group with
// CSRF middleware. Hosted setup never opts the tenant into recurring charges.
func RegisterHostedRenewalRoutes(group *gin.RouterGroup, service billingrenew.EnrollmentService) {
	h := hostedRenewalHandler{service: service}
	r := group.Group("/billing/auto-renewal/authorizations", middleware.RequireRole("owner", "admin", "billing"))
	r.GET("", h.list)
	r.GET("/:id", h.get)
	limit := middleware.RateLimitWindow(10, time.Minute, middleware.TenantRateKey)
	r.POST("", limit, h.start)
	r.POST("/:id/complete", limit, h.complete)
	r.POST("/:id/reconcile", limit, h.reconcile)
	r.POST("/:id/cancel", limit, h.cancel)
}

type hostedRenewalHandler struct {
	service billingrenew.EnrollmentService
}

func hostedPrincipal(c *gin.Context) (int64, int64, bool) {
	tenant, tok := middleware.GetTenantID(c)
	actor, aok := middleware.GetUserID(c)
	if !tok || !aok || tenant <= 0 || actor <= 0 {
		authError(c, 401, "UNAUTHORIZED")
		return 0, 0, false
	}
	return tenant, actor, true
}
func hostedHTTPError(c *gin.Context, e error) {
	if errors.Is(e, pgx.ErrNoRows) {
		authError(c, 404, "NOT_FOUND")
		return
	}
	renewalHTTPError(c, e)
}
func (h hostedRenewalHandler) list(c *gin.Context) {
	tenant, actor, ok := hostedPrincipal(c)
	if !ok {
		return
	}
	out, e := h.service.ListHostedSetups(c, tenant, actor, c.Query("request_key"))
	if e != nil {
		hostedHTTPError(c, e)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"authorizations": out})
}
func (h hostedRenewalHandler) get(c *gin.Context) {
	tenant, actor, ok := hostedPrincipal(c)
	if !ok {
		return
	}
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	out, e := h.service.GetHostedSetup(c, tenant, actor, id)
	if e != nil {
		hostedHTTPError(c, e)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"authorization": out})
}
func (h hostedRenewalHandler) start(c *gin.Context) {
	tenant, actor, ok := hostedPrincipal(c)
	if !ok {
		return
	}
	var input struct {
		MerchantID  int64  `json:"merchant_id"`
		QuoteDigest string `json:"quote_digest"`
		Confirmed   bool   `json:"confirmed"`
	}
	if !importDecode(c, &input, 8192) || input.MerchantID <= 0 {
		if !c.Writer.Written() {
			authError(c, 400, "INVALID_PARAMS")
		}
		return
	}
	key := c.GetHeader("Idempotency-Key")
	middleware.AfterCommit(c, func() {
		ctx, cancel := boundedProviderContext(c)
		defer cancel()
		out, e := h.service.StartHostedSetup(ctx, tenant, actor, input.MerchantID, key, input.QuoteDigest, input.Confirmed)
		if e != nil {
			hostedHTTPError(c, e)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(200, gin.H{"authorization": out})
	})
}
func (h hostedRenewalHandler) mutate(c *gin.Context, method func(int64, int64, int64, int64) (billingrenew.HostedSetup, error)) {
	tenant, actor, ok := hostedPrincipal(c)
	if !ok {
		return
	}
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	var input struct {
		Version int64 `json:"version"`
	}
	if e != nil || id <= 0 || !importDecode(c, &input, 8192) || input.Version <= 0 {
		if !c.Writer.Written() {
			authError(c, 400, "INVALID_PARAMS")
		}
		return
	}
	middleware.AfterCommit(c, func() {
		out, e := method(tenant, actor, id, input.Version)
		if e != nil {
			hostedHTTPError(c, e)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(200, gin.H{"authorization": out})
	})
}
func (h hostedRenewalHandler) complete(c *gin.Context) {
	h.mutate(c, func(t, a, id, v int64) (billingrenew.HostedSetup, error) {
		ctx, cancel := boundedProviderContext(c)
		defer cancel()
		return h.service.CompleteHostedSetup(ctx, t, a, id, v)
	})
}
func (h hostedRenewalHandler) reconcile(c *gin.Context) {
	h.mutate(c, func(t, a, id, v int64) (billingrenew.HostedSetup, error) {
		ctx, cancel := boundedProviderContext(c)
		defer cancel()
		return h.service.ReconcileHostedSetup(ctx, t, a, id, v)
	})
}
func (h hostedRenewalHandler) cancel(c *gin.Context) {
	h.mutate(c, func(t, a, id, v int64) (billingrenew.HostedSetup, error) {
		return h.service.CancelHostedSetup(c.Request.Context(), t, a, id, v)
	})
}

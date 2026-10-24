package handlers

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

// CouponReissueHandler replaces a verified lost or refunded redemption only by an audited operator decision.
type CouponReissueHandler struct{ Store *store.Queries }

// List exposes immutable replacement facts to platform operators.
func (h *CouponReissueHandler) List(c *gin.Context) {
	rows, err := requestStore(c, h.Store).PlatformListCouponReissues(c)
	if supportError(c, err) {
		return
	}
	c.JSON(200, gin.H{"coupons": rows})
}

// Issue clones the original value, binds the replacement to its tenant and leaves all entitlements unchanged.
func (h *CouponReissueHandler) Issue(c *gin.Context) {
	tenant, ok := marketingID(c, "id")
	if !ok {
		return
	}
	actor, ok := middleware.GetUserID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	var input struct {
		SourceCode string `json:"source_code"`
		Reason     string `json:"reason"`
		ValidUntil string `json:"valid_until"`
	}
	if c.ShouldBindJSON(&input) != nil || len(strings.TrimSpace(input.Reason)) < 10 || len(input.Reason) > 1000 || len(input.SourceCode) < 1 || len(input.SourceCode) > 200 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	until, err := time.Parse(time.RFC3339, input.ValidUntil)
	if err != nil || !until.After(time.Now()) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	q := requestStore(c, h.Store)
	if _, scoped := middleware.QueriesFromCtx(c); !scoped {
		authError(c, 503, "AUDIT_UNAVAILABLE")
		return
	}
	if err = q.PlatformLockBillingTenant(c, tenant); supportError(c, err) {
		return
	}
	middleware.SetPlatformAuditTenant(c, tenant)
	existing, err := q.PlatformGetCouponReissue(c, store.PlatformGetCouponReissueParams{SourceCouponCode: pgtype.Text{String: input.SourceCode, Valid: true}, SourceTenantID: pgtype.Int8{Int64: tenant, Valid: true}})
	if err == nil {
		c.JSON(200, gin.H{"coupon": existing, "idempotent": true, "entitlements_changed": false})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		supportError(c, err)
		return
	}
	raw := make([]byte, 24)
	if _, err = rand.Read(raw); err != nil {
		authError(c, 500, "INTERNAL_ERROR")
		return
	}
	issued, err := q.PlatformIssueReplacementCoupon(c, store.PlatformIssueReplacementCouponParams{NewCode: "REISSUE-" + base64.RawURLEncoding.EncodeToString(raw), TenantID: tenant, SourceCode: input.SourceCode, Reason: pgtype.Text{String: input.Reason, Valid: true}, ActorID: pgtype.Int8{Int64: actor, Valid: true}, ValidUntil: pgtype.Timestamptz{Time: until, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 409, "COUPON_REISSUE_NOT_ELIGIBLE")
		return
	}
	if supportError(c, err) {
		return
	}
	middleware.SetPlatformAuditDetail(c, map[string]any{"reason": input.Reason, "source_coupon_code": input.SourceCode, "replacement_code": issued.Code, "valid_until": until.Format(time.RFC3339), "entitlements_changed": false})
	c.JSON(201, gin.H{"coupon": issued, "entitlements_changed": false})
}

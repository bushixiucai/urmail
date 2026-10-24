package handlers

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

// BillingCalendarAdminHandler verifies missing legacy facts without rewriting paid terms.
type BillingCalendarAdminHandler struct{ Store *store.Queries }

func (h *BillingCalendarAdminHandler) response(c *gin.Context, q *store.Queries, tenantID int64) {
	if _, err := q.GetTenantByID(c, tenantID); supportError(c, err) {
		return
	}
	middleware.SetPlatformAuditTenant(c, tenantID)
	calendar, err := q.GetBillingCalendar(c, tenantID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		supportError(c, err)
		return
	}
	var result any
	review := false
	if err == nil {
		result = gin.H{"timezone": calendar.Timezone, "first_paid_at": calendar.FirstPaidAt, "anchor_at": calendar.AnchorAt, "version": calendar.Version, "source": calendar.Source}
	} else {
		required, loadErr := q.BillingCalendarReviewRequired(c, tenantID)
		if supportError(c, loadErr) {
			return
		}
		review = required
	}
	timezone, err := q.GetBillingBusinessTimezone(c, tenantID)
	if supportError(c, err) {
		return
	}
	c.JSON(200, gin.H{"tenant_id": tenantID, "calendar": result, "review_required": review, "current_business_timezone": timezone})
}

// Get returns a read-only existing calendar or the explicit legacy review state.
func (h *BillingCalendarAdminHandler) Get(c *gin.Context) {
	tenantID, ok := marketingID(c, "id")
	if !ok {
		return
	}
	h.response(c, requestStore(c, h.Store), tenantID)
}

// Put initializes exactly one missing calendar after an audited platform decision.
func (h *BillingCalendarAdminHandler) Put(c *gin.Context) {
	tenantID, ok := marketingID(c, "id")
	if !ok {
		return
	}
	actor, ok := middleware.GetUserID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	raw, scoped := middleware.QueriesFromCtx(c)
	q, concrete := raw.(*store.Queries)
	if !scoped || !concrete || q == nil {
		authError(c, 503, "AUDIT_UNAVAILABLE")
		return
	}
	var input struct {
		Timezone    string `json:"timezone"`
		FirstPaidAt string `json:"first_paid_at"`
		Reason      string `json:"reason"`
	}
	if c.Request.ContentLength > 8192 || c.ShouldBindJSON(&input) != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if utf8.RuneCountInString(input.Reason) < 20 || utf8.RuneCountInString(input.Reason) > 1000 || len(input.Timezone) > 64 || input.Timezone == "Local" {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if _, err := time.LoadLocation(input.Timezone); err != nil || input.Timezone == "" {
		authError(c, 400, "BILLING_CALENDAR_INVALID")
		return
	}
	firstPaidAt, err := time.Parse(time.RFC3339, input.FirstPaidAt)
	if err != nil || firstPaidAt.IsZero() || firstPaidAt.After(time.Now()) {
		authError(c, 400, "BILLING_CALENDAR_INVALID")
		return
	}
	allowed, err := q.CheckPlatformAdmin(c, actor)
	if supportError(c, err) {
		return
	}
	if !allowed {
		authError(c, 403, "FORBIDDEN")
		return
	}
	if err = q.PlatformLockBillingTenant(c, tenantID); supportError(c, err) {
		return
	}
	if _, err = q.GetTenantByID(c, tenantID); supportError(c, err) {
		return
	}
	middleware.SetPlatformAuditTenant(c, tenantID)
	if _, err = q.GetBillingCalendar(c, tenantID); err == nil {
		authError(c, 409, "BILLING_CALENDAR_ALREADY_SET")
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		supportError(c, err)
		return
	}
	required, err := q.BillingCalendarReviewRequired(c, tenantID)
	if supportError(c, err) {
		return
	}
	if !required {
		authError(c, 409, "BILLING_CALENDAR_REVIEW_NOT_REQUIRED")
		return
	}
	row, err := q.PlatformInitializeBillingCalendar(c, store.PlatformInitializeBillingCalendarParams{TenantID: tenantID, Timezone: input.Timezone, FirstPaidAt: pgtype.Timestamptz{Time: firstPaidAt, Valid: true}, AnchorAt: pgtype.Timestamptz{Time: firstPaidAt, Valid: true}, Source: "manual_legacy", ReviewReason: input.Reason, CreatedBy: pgtype.Int8{Int64: actor, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 409, "BILLING_CALENDAR_ALREADY_SET")
		return
	}
	if supportError(c, err) {
		return
	}
	middleware.SetPlatformAuditDetail(c, map[string]any{"reason": input.Reason, "timezone": row.Timezone, "first_paid_at": firstPaidAt.UTC().Format(time.RFC3339Nano), "source": row.Source, "calendar_version": row.Version})
	h.response(c, q, tenantID)
}

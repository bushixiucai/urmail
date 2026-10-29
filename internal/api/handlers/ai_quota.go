package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

type aiQuotaResolution struct {
	Source      string
	Limits      aiPlanLimits
	Start, End  pgtype.Timestamptz
	Timezone    string
	Independent bool
}

func aiQuotaMonth(now time.Time, zone string) (time.Time, time.Time, error) {
	location, err := time.LoadLocation(zone)
	if err != nil || zone == "" || zone == "Local" {
		return time.Time{}, time.Time{}, errAIBudget
	}
	local := now.In(location)
	start := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, location)
	end := time.Date(local.Year(), local.Month()+1, 1, 0, 0, 0, 0, location)
	return start, end, nil
}

// resolveAIQuota never rewrites mail entitlement or billing facts. Explicit
// host caps cover BYO/local usage by a frozen calendar window; an unchanged
// finite-plan quota remains the compatibility path when no caps are configured.
func resolveAIQuota(ctx context.Context, q *store.Queries, cfg store.AiProviderConfigs, tenant int64, now time.Time) (aiQuotaResolution, error) {
	result := aiQuotaResolution{Source: "unconfigured", Timezone: cfg.QuotaTimezone}
	if cfg.Mode != "platform" && cfg.NonplatformCallLimit > 0 && cfg.NonplatformMailboxCallLimit > 0 && cfg.NonplatformConcurrencyLimit > 0 {
		start, end, err := aiQuotaMonth(now, cfg.QuotaTimezone)
		if err != nil {
			return result, err
		}
		result.Source = "configured_nonplatform"
		result.Independent = true
		result.Start = pgtype.Timestamptz{Time: start, Valid: true}
		result.End = pgtype.Timestamptz{Time: end, Valid: true}
		result.Limits.AI.Calls = cfg.NonplatformCallLimit
		result.Limits.AI.MailboxCalls = cfg.NonplatformMailboxCallLimit
		result.Limits.AI.Concurrency = cfg.NonplatformConcurrencyLimit
		result.Limits.AI.Tokens = math.MaxInt64
		result.Limits.AI.Cost = math.MaxInt64
		return result, nil
	}
	plan, err := q.GetEffectivePlan(ctx, tenant)
	if err != nil {
		return result, errAIBudget
	}
	if json.Unmarshal(plan.Features, &result.Limits) != nil || result.Limits.AI.Calls <= 0 || result.Limits.AI.Concurrency <= 0 {
		return result, errAIBudget
	}
	if result.Limits.AI.MailboxCalls <= 0 {
		result.Limits.AI.MailboxCalls = result.Limits.AI.Calls
	}
	sub, err := q.GetActiveSubscriptionByTenant(ctx, tenant)
	if err != nil || !sub.PeriodStart.Valid || !sub.PeriodEnd.Valid || !sub.PeriodEnd.Time.After(now) {
		return result, errAIBudget
	}
	result.Start, result.End = sub.PeriodStart, sub.PeriodEnd
	if term, e := q.GetCurrentSubscriptionTerm(ctx, tenant); e == nil {
		result.Start, result.End = term.BillingPeriodStart, term.PeriodEnd
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return result, e
	}
	result.Source = "plan"
	return result, nil
}
func aiQuotaLocation(ctx context.Context, q *store.Queries, cfg store.AiProviderConfigs, tenant int64) (*time.Location, error) {
	zone := cfg.QuotaTimezone
	if zone == "" {
		var err error
		zone, err = q.GetBillingBusinessTimezone(ctx, tenant)
		if err != nil {
			return nil, err
		}
	}
	location, err := time.LoadLocation(zone)
	if err != nil || zone == "" || zone == "Local" {
		return nil, errAIBudget
	}
	return location, nil
}
func aiConfigurationQuotaFields(cfg store.AiProviderConfigs) gin.H {
	return gin.H{"nonplatform_call_limit": cfg.NonplatformCallLimit, "nonplatform_mailbox_call_limit": cfg.NonplatformMailboxCallLimit, "nonplatform_concurrency_limit": cfg.NonplatformConcurrencyLimit, "quota_timezone": cfg.QuotaTimezone, "daily_token_limit": cfg.DailyTokenLimit, "daily_cost_micros": cfg.DailyCostMicros, "monthly_token_limit": cfg.MonthlyTokenLimit, "monthly_cost_micros": cfg.MonthlyCostMicros}
}
func writeAIQuotaStatus(c *gin.Context, q *store.Queries, cfg store.AiProviderConfigs, tenant, mailbox int64) error {
	now := time.Now().Truncate(time.Microsecond)
	location, err := aiQuotaLocation(c, q, cfg, tenant)
	if err != nil {
		return err
	}
	local := now.In(location)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	month := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, location)
	daily, err := budgetUsage(c, q, tenant, day, now)
	if err != nil {
		return err
	}
	monthly, err := budgetUsage(c, q, tenant, month, now)
	if err != nil {
		return err
	}
	quota, quotaErr := resolveAIQuota(c, q, cfg, tenant, now)
	if quotaErr != nil && !errors.Is(quotaErr, errAIBudget) {
		return quotaErr
	}
	entitlement := gin.H{"source": quota.Source, "available": quotaErr == nil && cfg.Enabled, "call_limit": int64(0), "mailbox_call_limit": int64(0), "concurrency_limit": int64(0), "timezone": location.String(), "period_start": nil, "period_end": nil, "calls_used": int64(0), "calls_remaining": int64(0), "mailbox_calls_used": nil, "mailbox_calls_remaining": nil, "concurrent_reserved": int64(0)}
	if quotaErr == nil {
		usage, e := q.PlatformAIQuotaUsage(c, store.PlatformAIQuotaUsageParams{TenantID: tenant, MailboxID: mailbox, IndependentWindow: quota.Independent, PeriodStart: quota.Start, PeriodEnd: quota.End})
		if e != nil {
			return e
		}
		entitlement["call_limit"] = quota.Limits.AI.Calls
		entitlement["mailbox_call_limit"] = quota.Limits.AI.MailboxCalls
		entitlement["concurrency_limit"] = quota.Limits.AI.Concurrency
		entitlement["period_start"] = quota.Start
		entitlement["period_end"] = quota.End
		entitlement["calls_used"] = usage.CallsUsed
		entitlement["calls_remaining"] = max(int64(0), quota.Limits.AI.Calls-usage.CallsUsed)
		entitlement["concurrent_reserved"] = usage.ConcurrentReserved
		if mailbox > 0 {
			entitlement["mailbox_calls_used"] = usage.MailboxCallsUsed
			entitlement["mailbox_calls_remaining"] = max(int64(0), quota.Limits.AI.MailboxCalls-usage.MailboxCallsUsed)
		}
	}
	dailyCost, monthlyCost, dailyLimit, monthlyLimit := daily.DailyCostMicros, monthly.MonthlyCostMicros, zeroUnlimited(cfg.DailyCostMicros), zeroUnlimited(cfg.MonthlyCostMicros)
	if cfg.Mode != "platform" {
		dailyCost, monthlyCost, dailyLimit, monthlyLimit = 0, 0, 0, 0
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"enabled": cfg.Enabled, "mode": cfg.Mode, "cost_currency": cfg.CostCurrency, "quota_timezone": location.String(), "entitlement": entitlement,
		"daily":   gin.H{"tokens_used": daily.DailyTokens, "tokens_limit": zeroUnlimited(cfg.DailyTokenLimit), "cost_micros_used": dailyCost, "cost_micros_limit": dailyLimit},
		"monthly": gin.H{"tokens_used": monthly.MonthlyTokens, "tokens_limit": zeroUnlimited(cfg.MonthlyTokenLimit), "cost_micros_used": monthlyCost, "cost_micros_limit": monthlyLimit}})
	return nil
}

// PlatformBudgetGet exposes quota governance without credentials or mail data.
func (h *AIConfigurationHandler) PlatformBudgetGet(c *gin.Context) { h.servePanelAIQuota(c, true) }

// BudgetGet exposes the current tenant's read-only AI admission limits.
func (h *AIConfigurationHandler) BudgetGet(c *gin.Context) { h.servePanelAIQuota(c, false) }

// TenantBudgetGet is the explicit tenant route entrypoint.
func (h *AIConfigurationHandler) TenantBudgetGet(c *gin.Context) { h.BudgetGet(c) }
func (h *AIConfigurationHandler) servePanelAIQuota(c *gin.Context, platform bool) {
	if h == nil || h.Pool == nil {
		authError(c, 503, "AI_DISABLED")
		return
	}
	actor, ok := middleware.GetUserID(c)
	if !ok || actor <= 0 {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	tenant, ok := middleware.GetTenantID(c)
	if platform {
		parsed, err := strconv.ParseInt(c.Param("id"), 10, 64)
		tenant, ok = parsed, err == nil && parsed > 0
	}
	if !ok {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	tx, err := h.Pool.PlatformTx(c)
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	if err = q.PlatformLockBillingTenant(c, tenant); err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	if platform {
		_, err = q.PlatformLockRecurringAdminActor(c, actor)
	} else {
		_, err = q.PlatformLockAIQuotaTenantReader(c, store.PlatformLockAIQuotaTenantReaderParams{TenantID: tenant, ID: actor})
	}
	if err != nil {
		authError(c, http.StatusForbidden, "FORBIDDEN")
		return
	}
	cfg, err := q.PlatformGetAIProvider(c, tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(200, gin.H{"enabled": false, "entitlement": gin.H{"source": "unconfigured", "available": false, "call_limit": 0, "mailbox_call_limit": 0, "concurrency_limit": 0}})
		return
	} else if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	if err = writeAIQuotaStatus(c, q, cfg, tenant, 0); err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
}
func serveMailboxAIQuota(c *gin.Context, pool *db.Pool) {
	if pool == nil {
		authError(c, 503, "AI_DISABLED")
		return
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok || session.TenantID <= 0 {
		authError(c, 401, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	tx, err := pool.PlatformTx(c)
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	if err = q.PlatformLockBillingTenant(c, session.TenantID); err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	live, sessionErr := q.PlatformGetMailboxSession(c, session.TokenHash)
	if sessionErr != nil || live.TenantID != session.TenantID || live.MailboxID != session.MailboxID || !live.ExpiresAt.Time.After(time.Now()) {
		authError(c, 401, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	cfg, err := q.PlatformGetAIProvider(c, session.TenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 409, "AI_DISABLED")
		return
	} else if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	if err = writeAIQuotaStatus(c, q, cfg, session.TenantID, session.MailboxID); err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
}

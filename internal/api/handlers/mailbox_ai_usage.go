package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/ai"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

// ErrAIBudgetExceeded is returned BEFORE any provider call when the tenant
// daily or monthly budget cannot absorb the worst-case reservation.
var ErrAIBudgetExceeded = errors.New("ai tenant budget exceeded")

// AIBudget is the tenant-level daily/monthly usage budget stored on
// ai_provider_configs (0 means unlimited). The platform plan limits enforced
// inside MailboxAIService apply independently of this tenant budget.
type AIBudget struct {
	DailyTokens       int64
	DailyCostMicros   int64
	MonthlyTokens     int64
	MonthlyCostMicros int64
}

// Exceeded reports whether adding the worst-case reservation to the window
// usage crosses either budget. Pure boundary logic: exactly reaching the
// limit is allowed, strictly crossing it is refused.
func (b AIBudget) Exceeded(used, reserved AIBudget) bool {
	if b.DailyTokens > 0 && used.DailyTokens+reserved.DailyTokens > b.DailyTokens {
		return true
	}
	if b.DailyCostMicros > 0 && used.DailyCostMicros+reserved.DailyCostMicros > b.DailyCostMicros {
		return true
	}
	if b.MonthlyTokens > 0 && used.MonthlyTokens+reserved.MonthlyTokens > b.MonthlyTokens {
		return true
	}
	if b.MonthlyCostMicros > 0 && used.MonthlyCostMicros+reserved.MonthlyCostMicros > b.MonthlyCostMicros {
		return true
	}
	return false
}

// Reservation identifies one written reservation row.
type Reservation struct {
	id      int64
	tenant  int64
	kind    string
	created bool
	pool    *db.Pool
}

// AISearchAskAccounting records search-ask usage through the existing
// ai_usage_requests machinery without the trusted-bridge ticket flow. The
// bridge service (MailboxAIService) serializes on a caller-supplied
// idempotency ticket that the mailbox-session search-ask endpoint does not
// have; this path instead writes one reservation row per request under a
// freshly generated unique request key and reuses PlatformFinishAIRequest
// for settlement.
type AISearchAskAccounting struct {
	Pool *db.Pool
}

// ErrNotAccounted reports that AI is not configured or disabled: nothing
// is accounted and the caller proceeds to normal AI_UNAVAILABLE surfacing.
var ErrNotAccounted = errors.New("ai not accounted")

// Reserve applies the tenant budget and, when it fits, writes one reservation
// row. ErrNotAccounted means AI is not configured or disabled: nothing
// is accounted and the caller proceeds to the normal AI_UNAVAILABLE
// surfacing. ErrAIBudgetExceeded is returned before any provider call.
func (h AISearchAskAccounting) Reserve(ctx context.Context, tenantID, mailboxID int64, question string) (*Reservation, error) {
	if h.Pool == nil {
		return nil, ErrNotAccounted
	}
	tx, err := h.Pool.PlatformTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	if err = q.PlatformExpireAIReservations(ctx, tenantID); err != nil {
		return nil, err
	}
	cfg, err := q.PlatformGetAIProvider(ctx, tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotAccounted
	}
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, ErrNotAccounted
	}
	reservedTokens := int64(len(question)) + 4096 // UTF-8 bound plus answer headroom.
	reservedCost := int64(0)
	if cfg.Mode == "platform" {
		if reservedCost, err = aiCost(reservedTokens, 1024, cfg.InputMicrosPerMillion, cfg.OutputMicrosPerMillion); err != nil {
			return nil, err
		}
	}
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	dayUsed, err := budgetUsage(ctx, q, tenantID, dayStart, now)
	if err != nil {
		return nil, err
	}
	monthUsed, err := budgetUsage(ctx, q, tenantID, monthStart, now)
	if err != nil {
		return nil, err
	}
	budget := AIBudget{DailyTokens: cfg.DailyTokenLimit, DailyCostMicros: cfg.DailyCostMicros, MonthlyTokens: cfg.MonthlyTokenLimit, MonthlyCostMicros: cfg.MonthlyCostMicros}
	reserved := AIBudget{DailyTokens: reservedTokens, DailyCostMicros: reservedCost, MonthlyTokens: reservedTokens, MonthlyCostMicros: reservedCost}
	if budget.Exceeded(dayUsed, AIBudget{DailyTokens: reserved.DailyTokens, DailyCostMicros: reserved.DailyCostMicros}) ||
		budget.Exceeded(monthUsed, AIBudget{MonthlyTokens: reserved.MonthlyTokens, MonthlyCostMicros: reserved.MonthlyCostMicros}) {
		ai.BudgetExceededTotal.Inc()
		return nil, ErrAIBudgetExceeded
	}
	digest := sha256.Sum256([]byte(question))
	key := make([]byte, 16)
	if _, err = rand.Read(key); err != nil {
		return nil, err
	}
	row, err := q.PlatformReserveMailboxAIRequest(ctx, store.PlatformReserveMailboxAIRequestParams{
		TenantID: tenantID, MailboxID: mailboxID, RequestKey: "ask-" + hex.EncodeToString(key), InputDigest: hex.EncodeToString(digest[:]),
		PeriodStart: pgtype.Timestamptz{Time: dayStart, Valid: true}, PeriodEnd: pgtype.Timestamptz{Time: dayStart.AddDate(0, 0, 1), Valid: true}, Mode: cfg.Mode, Provider: cfg.Provider, Model: cfg.Model,
		CostCurrency: cfg.CostCurrency, InputRate: cfg.InputMicrosPerMillion, OutputRate: cfg.OutputMicrosPerMillion,
		ReservedTokens: reservedTokens, ReservedCostMicros: reservedCost,
	})
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &Reservation{id: row.ID, tenant: tenantID, kind: "search_ask", created: true, pool: h.Pool}, nil
}

// Settle records observed usage best-effort in a detached context: a
// recording failure is logged and counted as a metric, never propagated into
// the already delivered answer.
func (r *Reservation) Settle(providerErr error, usageKnown bool, promptTokens, outputTokens int64, costFn func(prompt, output int64) (int64, error)) {
	if r == nil || !r.created || r.pool == nil {
		return
	}
	tenant, id, pool, kind := r.tenant, r.id, r.pool, r.kind
	go func() {
		settleCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		status := "uncertain"
		var actualTokens, actualCost int64
		var record bool
		if providerErr == nil && usageKnown && promptTokens >= 0 && outputTokens >= 0 {
			cost, costErr := costFn(promptTokens, outputTokens)
			if costErr == nil {
				actualTokens, actualCost = promptTokens+outputTokens, cost
				status = "completed"
				record = true
			}
		} else if providerErr == nil {
			// Unknown usage stays charged at its reservation.
			status = "completed"
		}
		var tokensField, costField pgtype.Int8
		if record {
			tokensField = pgtype.Int8{Int64: actualTokens, Valid: true}
			costField = pgtype.Int8{Int64: actualCost, Valid: true}
		}
		finished, finishErr := store.New(pool).PlatformFinishAIRequest(settleCtx, store.PlatformFinishAIRequestParams{TenantID: tenant, ID: id, Status: status, ActualTokens: tokensField, ActualCostMicros: costField})
		if finishErr != nil || finished != 1 {
			log.Printf("urmail: ai usage recording failed tenant=%d request=%d err=%v rows=%d", tenant, id, finishErr, finished)
			ai.RecordOutcome(kind, "record_failed")
		}
	}()
}

// budgetUsage sums one usage window through the accounting queries.
func budgetUsage(ctx context.Context, q *store.Queries, tenantID int64, start, end time.Time) (AIBudget, error) {
	row, err := q.PlatformSumAIUsageBetween(ctx, store.PlatformSumAIUsageBetweenParams{TenantID: tenantID, WindowStart: pgtype.Timestamptz{Time: start, Valid: true}, WindowEnd: pgtype.Timestamptz{Time: end, Valid: true}})
	if err != nil {
		return AIBudget{}, err
	}
	return AIBudget{DailyTokens: row.Tokens, DailyCostMicros: row.CostMicros, MonthlyTokens: row.Tokens, MonthlyCostMicros: row.CostMicros}, nil
}

// BudgetStatus serves GET /api/v1/mailbox/ai/budget: the server-side
// authoritative usage-versus-budget view for the tenant UI.
func (h *AISearchAskAccounting) BudgetStatus(c *gin.Context) {
	if h == nil || h.Pool == nil {
		authError(c, http.StatusServiceUnavailable, "AI_DISABLED")
		return
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok || session.TenantID <= 0 {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	tenant := session.TenantID
	q := store.New(h.Pool)
	cfg, err := q.PlatformGetAIProvider(c.Request.Context(), tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, http.StatusConflict, "AI_DISABLED")
		return
	}
	if err != nil {
		authError(c, http.StatusBadGateway, "DB_ERROR")
		return
	}
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	daily, err := budgetUsage(c.Request.Context(), q, tenant, dayStart, now)
	if err != nil {
		authError(c, http.StatusBadGateway, "DB_ERROR")
		return
	}
	monthly, err := budgetUsage(c.Request.Context(), q, tenant, monthStart, now)
	if err != nil {
		authError(c, http.StatusBadGateway, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":       cfg.Enabled,
		"cost_currency": cfg.CostCurrency,
		"daily": gin.H{
			"tokens_used":       daily.DailyTokens,
			"tokens_limit":      zeroUnlimited(cfg.DailyTokenLimit),
			"cost_micros_used":  daily.DailyCostMicros,
			"cost_micros_limit": zeroUnlimited(cfg.DailyCostMicros),
		},
		"monthly": gin.H{
			"tokens_used":       monthly.MonthlyTokens,
			"tokens_limit":      zeroUnlimited(cfg.MonthlyTokenLimit),
			"cost_micros_used":  monthly.MonthlyCostMicros,
			"cost_micros_limit": zeroUnlimited(cfg.MonthlyCostMicros),
		},
	})
}

// PlatformBudgetPut lets a platform operator set the tenant AI budgets.
func (h *AIConfigurationHandler) PlatformBudgetPut(c *gin.Context) {
	if h == nil || h.Pool == nil {
		authError(c, http.StatusServiceUnavailable, "AI_DISABLED")
		return
	}
	tid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	var input struct {
		DailyTokens   int64 `json:"daily_token_limit"`
		DailyCost     int64 `json:"daily_cost_micros"`
		MonthlyTokens int64 `json:"monthly_token_limit"`
		MonthlyCost   int64 `json:"monthly_cost_micros"`
	}
	if err != nil || tid <= 0 || c.ShouldBindJSON(&input) != nil || input.DailyTokens < 0 || input.DailyCost < 0 || input.MonthlyTokens < 0 || input.MonthlyCost < 0 ||
		input.DailyTokens > 9007199254740991 || input.DailyCost > 9007199254740991 || input.MonthlyTokens > 9007199254740991 || input.MonthlyCost > 9007199254740991 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	if err = store.New(h.Pool).PlatformSetAIBudget(c.Request.Context(), store.PlatformSetAIBudgetParams{TenantID: tid, DailyTokenLimit: input.DailyTokens, DailyCostMicros: input.DailyCost, MonthlyTokenLimit: input.MonthlyTokens, MonthlyCostMicros: input.MonthlyCost}); err != nil {
		authError(c, http.StatusBadGateway, "DB_ERROR")
		return
	}
	c.Status(http.StatusNoContent)
}

func zeroUnlimited(limit int64) int64 {
	if limit == 0 {
		return -1 // -1 means unlimited in the API, avoiding a null/0 ambiguity.
	}
	return limit
}

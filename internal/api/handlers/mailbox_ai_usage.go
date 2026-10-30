package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"math"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/ai"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/billing"
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
	exceeds := func(limit, consumed, additional int64) bool {
		return limit > 0 && (consumed > limit || additional > limit-consumed)
	}
	return exceeds(b.DailyTokens, used.DailyTokens, reserved.DailyTokens) || exceeds(b.DailyCostMicros, used.DailyCostMicros, reserved.DailyCostMicros) || exceeds(b.MonthlyTokens, used.MonthlyTokens, reserved.MonthlyTokens) || exceeds(b.MonthlyCostMicros, used.MonthlyCostMicros, reserved.MonthlyCostMicros)
}

// checkAIProviderBudget must run under the caller's tenant billing lock and
// before its durable reservation. Both native operations and grounded answers
// consume the same ledger; unknown outcomes retain their reserved usage.
func checkAIProviderBudget(ctx context.Context, q *store.Queries, cfg store.AiProviderConfigs, tenantID, reservedTokens, reservedCost int64, now time.Time) error {
	location, err := aiQuotaLocation(ctx, q, cfg, tenantID)
	if err != nil {
		return err
	}
	dayStart, monthStart, _, err := billing.CalendarBudgetBounds(now, location.String())
	if err != nil {
		return err
	}
	dayUsed, err := budgetUsage(ctx, q, tenantID, dayStart, now)
	if err != nil {
		return err
	}
	monthUsed, err := budgetUsage(ctx, q, tenantID, monthStart, now)
	if err != nil {
		return err
	}
	budget := AIBudget{DailyTokens: cfg.DailyTokenLimit, DailyCostMicros: cfg.DailyCostMicros, MonthlyTokens: cfg.MonthlyTokenLimit, MonthlyCostMicros: cfg.MonthlyCostMicros}
	if cfg.Mode != "platform" {
		budget.DailyCostMicros = 0
		budget.MonthlyCostMicros = 0
	}
	reserved := AIBudget{DailyTokens: reservedTokens, DailyCostMicros: reservedCost, MonthlyTokens: reservedTokens, MonthlyCostMicros: reservedCost}
	used := AIBudget{DailyTokens: dayUsed.DailyTokens, DailyCostMicros: dayUsed.DailyCostMicros, MonthlyTokens: monthUsed.MonthlyTokens, MonthlyCostMicros: monthUsed.MonthlyCostMicros}
	if budget.Exceeded(used, reserved) {
		ai.BudgetExceededTotal.Inc()
		return ErrAIBudgetExceeded
	}
	return nil
}

// Reservation identifies one written reservation row.
type Reservation struct {
	id                    int64
	tenant                int64
	kind                  string
	mode                  string
	created               bool
	pool                  *db.Pool
	inputRate, outputRate int64
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
	return h.reserve(ctx, tenantID, mailboxID, question, nil)
}

// ReserveCustom applies the same call, concurrency and token governance without
// charging a user's own provider as platform-funded inference.
func (h AISearchAskAccounting) ReserveCustom(ctx context.Context, tenantID, mailboxID int64, question string, target *userAICustomConfig) (*Reservation, error) {
	if target == nil {
		return nil, errAITargetChanged
	}
	return h.reserve(ctx, tenantID, mailboxID, question, target)
}

func (h AISearchAskAccounting) reserve(ctx context.Context, tenantID, mailboxID int64, question string, custom *userAICustomConfig) (*Reservation, error) {
	if h.Pool == nil {
		if custom != nil {
			return nil, ai.ErrProviderDisabled
		}
		return nil, ErrNotAccounted
	}
	tx, err := h.Pool.PlatformTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	if err = q.PlatformLockBillingTenant(ctx, tenantID); err != nil {
		return nil, err
	}
	if err = q.PlatformExpireAIReservations(ctx, tenantID); err != nil {
		return nil, err
	}
	cfg, err := q.PlatformGetAIProvider(ctx, tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		if custom != nil {
			cfg = store.AiProviderConfigs{TenantID: tenantID, PersonalMode: "public"}
			err = nil
		} else {
			return nil, ErrNotAccounted
		}
	}
	if err != nil {
		return nil, err
	}
	if custom == nil && !cfg.Enabled {
		return nil, ErrNotAccounted
	}
	if custom != nil {
		cfg, err = customAIConfiguration(ctx, q, cfg, tenantID, mailboxID, custom, custom.Digest)
		if err != nil {
			return nil, err
		}
	}
	// Twenty excerpts of at most 500 bytes, 20 digit citation identifiers,
	// prompt framing and a 2048-token output all fit inside this bound.
	reservedTokens := int64(len(question)) + 16384
	reservedCost := int64(0)
	if cfg.Mode == "platform" {
		if reservedCost, err = aiCost(reservedTokens, 2048, cfg.InputMicrosPerMillion, cfg.OutputMicrosPerMillion); err != nil {
			return nil, err
		}
	}
	now := time.Now().Truncate(time.Microsecond)
	if custom == nil {
		if err = checkAIProviderBudget(ctx, q, cfg, tenantID, reservedTokens, reservedCost, now); err != nil {
			return nil, err
		}
	}

	digest := sha256.Sum256([]byte(question))
	key := make([]byte, 16)
	if _, err = rand.Read(key); err != nil {
		return nil, err
	}
	quota, err := resolveAIQuota(ctx, q, cfg, tenantID, now)
	if err != nil {
		if custom != nil {
			quota = aiQuotaResolution{
				Source:      "personal_byok",
				Independent: true,
				Start:       pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true},
				End:         pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true},
			}
			quota.Limits.AI.Calls = 60
			quota.Limits.AI.MailboxCalls = 60
			quota.Limits.AI.Concurrency = 5
			quota.Limits.AI.Tokens = math.MaxInt64
			quota.Limits.AI.Cost = math.MaxInt64
		} else {
			return nil, ErrAIBudgetExceeded
		}
	} else if custom != nil {
		quota.Limits.AI.Tokens = math.MaxInt64
		quota.Limits.AI.Cost = math.MaxInt64
		if quota.Limits.AI.Calls <= 0 || quota.Limits.AI.Calls > 60 {
			quota.Limits.AI.Calls = 60
			quota.Limits.AI.MailboxCalls = 60
		}
		if quota.Limits.AI.Concurrency <= 0 || quota.Limits.AI.Concurrency > 5 {
			quota.Limits.AI.Concurrency = 5
		}
	}
	limits := quota.Limits
	start, end := quota.Start, quota.End
	tokenLimit, costLimit := limits.AI.Tokens, limits.AI.Cost
	if cfg.Mode == "platform" {
		if tokenLimit <= 0 || costLimit <= 0 || limits.AI.Currency != cfg.CostCurrency {
			return nil, ErrAIBudgetExceeded
		}
	} else {
		tokenLimit, costLimit = math.MaxInt64, math.MaxInt64
	}
	row, err := q.PlatformReserveAIRequest(ctx, store.PlatformReserveAIRequestParams{
		TenantID: tenantID, MailboxID: mailboxID, RequestKey: "ask-" + hex.EncodeToString(key), InputDigest: hex.EncodeToString(digest[:]), AccountedAt: pgtype.Timestamptz{Time: now, Valid: true}, IndependentWindow: quota.Independent, PeriodStart: start, PeriodEnd: end, Mode: cfg.Mode, Provider: cfg.Provider, Model: cfg.Model, CostCurrency: cfg.CostCurrency, InputRate: cfg.InputMicrosPerMillion, OutputRate: cfg.OutputMicrosPerMillion, ReservedTokens: reservedTokens, ReservedCostMicros: reservedCost, CallLimit: limits.AI.Calls, MailboxCallLimit: limits.AI.MailboxCalls, ConcurrencyLimit: limits.AI.Concurrency, TokenLimit: tokenLimit, CostLimit: costLimit,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAIBudgetExceeded
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &Reservation{id: row.ID, tenant: tenantID, kind: "search_ask", mode: cfg.Mode, created: true, pool: h.Pool, inputRate: cfg.InputMicrosPerMillion, outputRate: cfg.OutputMicrosPerMillion}, nil
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
		} else if providerErr == nil && !usageKnown && r.mode != "platform" {
			// BYOK completion does not settle platform-provider spend. Unknown
			// platform usage must remain reviewable and charged at its reservation.
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
func (h *AISearchAskAccounting) BudgetStatus(c *gin.Context) { serveMailboxAIQuota(c, h.Pool) }

// PlatformBudgetPut lets a platform operator set the tenant AI budgets.
func (h *AIConfigurationHandler) PlatformBudgetPut(c *gin.Context) {
	if h == nil || h.Pool == nil {
		authError(c, 503, "AI_DISABLED")
		return
	}
	tenant, err := strconv.ParseInt(c.Param("id"), 10, 64)
	var input struct {
		DailyTokens   *int64 `json:"daily_token_limit"`
		DailyCost     *int64 `json:"daily_cost_micros"`
		MonthlyTokens *int64 `json:"monthly_token_limit"`
		MonthlyCost   *int64 `json:"monthly_cost_micros"`
		Calls         *int64 `json:"nonplatform_call_limit"`
		MailboxCalls  *int64 `json:"nonplatform_mailbox_call_limit"`
		Concurrency   *int64 `json:"nonplatform_concurrency_limit"`
	}
	if err != nil || tenant <= 0 || c.ShouldBindJSON(&input) != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	values := []*int64{input.DailyTokens, input.DailyCost, input.MonthlyTokens, input.MonthlyCost, input.Calls, input.MailboxCalls, input.Concurrency}
	supplied := false
	for _, value := range values {
		if value != nil {
			supplied = true
			if *value < 0 || *value > 9007199254740991 {
				authError(c, 400, "INVALID_PARAMS")
				return
			}
		}
	}
	hasCaps := input.Calls != nil || input.MailboxCalls != nil || input.Concurrency != nil
	if !supplied || (hasCaps && (input.Calls == nil || input.MailboxCalls == nil || input.Concurrency == nil)) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if hasCaps && (*input.Concurrency > 64 || ((*input.Calls == 0 || *input.MailboxCalls == 0 || *input.Concurrency == 0) && (*input.Calls != 0 || *input.MailboxCalls != 0 || *input.Concurrency != 0))) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	actor, ok := middleware.GetUserID(c)
	if !ok || actor <= 0 {
		authError(c, 401, "UNAUTHORIZED")
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
	if _, err = q.PlatformLockRecurringAdminActor(c, actor); err != nil {
		authError(c, 403, "FORBIDDEN")
		return
	}
	cfg, err := q.PlatformGetAIProvider(c, tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 404, "NOT_FOUND")
		return
	} else if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	assign := func(value *int64, previous int64) int64 {
		if value != nil {
			return *value
		}
		return previous
	}
	dailyTokens, dailyCost := assign(input.DailyTokens, cfg.DailyTokenLimit), assign(input.DailyCost, cfg.DailyCostMicros)
	monthlyTokens, monthlyCost := assign(input.MonthlyTokens, cfg.MonthlyTokenLimit), assign(input.MonthlyCost, cfg.MonthlyCostMicros)
	calls, mailboxCalls, concurrency := assign(input.Calls, cfg.NonplatformCallLimit), assign(input.MailboxCalls, cfg.NonplatformMailboxCallLimit), assign(input.Concurrency, cfg.NonplatformConcurrencyLimit)
	timezone := cfg.QuotaTimezone
	if timezone == "" && (calls > 0 || dailyTokens > 0 || dailyCost > 0 || monthlyTokens > 0 || monthlyCost > 0) {
		timezone, err = q.GetBillingBusinessTimezone(c, tenant)
		if err != nil {
			authError(c, 503, "DB_ERROR")
			return
		}
	}
	if err = q.PlatformSetAIBudget(c, store.PlatformSetAIBudgetParams{TenantID: tenant, DailyTokenLimit: dailyTokens, DailyCostMicros: dailyCost, MonthlyTokenLimit: monthlyTokens, MonthlyCostMicros: monthlyCost}); err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	if err = q.PlatformSetNonplatformAIQuota(c, store.PlatformSetNonplatformAIQuotaParams{TenantID: tenant, NonplatformCallLimit: calls, NonplatformMailboxCallLimit: mailboxCalls, NonplatformConcurrencyLimit: concurrency, QuotaTimezone: timezone}); err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	detail, _ := json.Marshal(gin.H{"daily_token_limit": dailyTokens, "daily_cost_micros": dailyCost, "monthly_token_limit": monthlyTokens, "monthly_cost_micros": monthlyCost, "nonplatform_call_limit": calls, "nonplatform_mailbox_call_limit": mailboxCalls, "nonplatform_concurrency_limit": concurrency, "quota_timezone": timezone})
	if _, err = q.CreateAuditLog(c, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: tenant, Valid: true}, ActorUserID: pgtype.Int8{Int64: actor, Valid: true}, Action: "UPDATE ai.budget", TargetType: pgtype.Text{String: "tenant", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(tenant, 10), Valid: true}, Detail: detail}); err != nil {
		authError(c, 503, "AUDIT_UNAVAILABLE")
		return
	}
	if err = tx.Commit(c); err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	c.Status(204)
}

func zeroUnlimited(limit int64) int64 {
	if limit == 0 {
		return -1 // -1 means unlimited in the API, avoiding a null/0 ambiguity.
	}
	return limit
}

// releaseUnused is called only when the request proves no provider generation
// was invoked. It cannot release uncertain outbound work or invent exact usage.
func (r *Reservation) releaseUnused() {
	if r == nil || !r.created || r.pool == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rows, err := store.New(r.pool).PlatformFinishAIRequest(ctx, store.PlatformFinishAIRequestParams{TenantID: r.tenant, ID: r.id, Status: "released", ActualTokens: pgtype.Int8{Int64: 0, Valid: true}, ActualCostMicros: pgtype.Int8{Int64: 0, Valid: true}})
		if err != nil || rows != 1 {
			log.Printf("urmail: unused ai reservation release failed tenant=%d request=%d err=%v rows=%d", r.tenant, r.id, err, rows)
			ai.RecordOutcome(r.kind, "record_failed")
		}
	}()
}

func (r *Reservation) observedCost(prompt, output int64) (int64, error) {
	if r == nil || r.mode != "platform" {
		return 0, nil
	}
	return aiCost(prompt, output, r.inputRate, r.outputRate)
}

package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/ai"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/store"
)

var errAIBudget = errors.New("AI budget unavailable")
var errAIIntent = errors.New("AI request already exists or differs")

// MailboxAIService reserves a worst-case budget before the single outbound call.
// It accepts a mailbox principal produced by the trusted bridge, never panel IDs.
type MailboxAIService struct {
	Pool                           *db.Pool
	Secrets                        SecretsBox
	CloudEndpoints, LocalEndpoints []string
	// Provider is an injection point for offline tests, never derived from input.
	Provider func(store.AiProviderConfigs) (ai.Provider, error)
}

type aiPlanLimits struct {
	AI struct {
		Calls        int64  `json:"calls"`
		Tokens       int64  `json:"tokens"`
		Cost         int64  `json:"cost_micros"`
		Concurrency  int64  `json:"concurrency"`
		MailboxCalls int64  `json:"mailbox_calls"`
		Currency     string `json:"cost_currency"`
	} `json:"ai"`
}

func aiCost(input, output, inputRate, outputRate int64) (int64, error) {
	if input < 0 || output < 0 || inputRate < 0 || outputRate < 0 {
		return 0, errAIBudget
	}
	total := new(big.Int).Mul(big.NewInt(input), big.NewInt(inputRate))
	total.Add(total, new(big.Int).Mul(big.NewInt(output), big.NewInt(outputRate)))
	total.Add(total, big.NewInt(999999))
	total.Quo(total, big.NewInt(1000000))
	if !total.IsInt64() {
		return 0, errAIBudget
	}
	return total.Int64(), nil
}

// Run serializes reservation and caches one exact mailbox operation.
func (s *MailboxAIService) Run(ctx context.Context, principal store.PlatformConsumeMailboxTicketRow, key string, input ai.OperationInput, expectedConfiguration ...string) (json.RawMessage, error) {
	return s.run(ctx, principal, key, input, nil, expectedConfiguration...)
}

// RunCustom uses the same durable intent, reservation and settlement ledger as
// managed AI, with zero platform cost and the tenant's nonplatform governance.
func (s *MailboxAIService) RunCustom(ctx context.Context, principal store.PlatformConsumeMailboxTicketRow, key string, input ai.OperationInput, target *userAICustomConfig, expected string) (json.RawMessage, error) {
	if target == nil {
		return nil, errAITargetChanged
	}
	return s.run(ctx, principal, key, input, target, expected)
}

func (s *MailboxAIService) run(ctx context.Context, principal store.PlatformConsumeMailboxTicketRow, key string, input ai.OperationInput, custom *userAICustomConfig, expectedConfiguration ...string) (json.RawMessage, error) {
	if s == nil || s.Pool == nil || len(key) < 16 || len(key) > 200 {
		return nil, ai.ErrProviderDisabled
	}
	targetDigest := ""
	if len(expectedConfiguration) == 1 {
		targetDigest = expectedConfiguration[0]
	}
	request, err := ai.BuildOperation(input)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(struct {
		TenantID, MailboxID int64
		Folder              string
		UID, UIDValidity    int64
		Input               ai.OperationInput
		TargetDigest        string
	}{principal.TenantID, principal.MailboxID, principal.Folder, principal.MessageUid, principal.UidValidity, input, targetDigest})
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	tx, err := s.Pool.PlatformTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	if err = q.PlatformLockBillingTenant(ctx, principal.TenantID); err != nil {
		return nil, err
	}
	if session, sessionErr := q.PlatformGetMailboxSession(ctx, principal.SessionHash); sessionErr != nil || session.TenantID != principal.TenantID || session.MailboxID != principal.MailboxID || !session.ExpiresAt.Time.After(time.Now()) || !mailboxprincipal.ValidReference(principal.Scope, principal.Folder, principal.MessageUid, principal.UidValidity) {
		return nil, ai.ErrUnauthorized
	}
	if err = q.PlatformExpireAIReservations(ctx, principal.TenantID); err != nil {
		return nil, err
	}
	cfg, configurationErr := q.PlatformGetAIProvider(ctx, principal.TenantID)
	if custom != nil {
		if configurationErr != nil && !errors.Is(configurationErr, pgx.ErrNoRows) {
			return nil, configurationErr
		}
		if errors.Is(configurationErr, pgx.ErrNoRows) {
			cfg = store.AiProviderConfigs{TenantID: principal.TenantID, PersonalMode: "public"}
			configurationErr = nil
		}
		cfg, err = customAIConfiguration(ctx, q, cfg, principal.TenantID, principal.MailboxID, custom, targetDigest)
		if err != nil {
			return nil, err
		}
	}
	existing, lookup := q.PlatformGetAIRequest(ctx, store.PlatformGetAIRequestParams{TenantID: principal.TenantID, RequestKey: key})
	if lookup == nil {
		if existing.InputDigest != hash || existing.MailboxID != principal.MailboxID {
			return nil, errAIIntent
		}
		if existing.Status == "completed" {
			if tx.Commit(ctx) != nil {
				return nil, errAIBudget
			}
			return existing.Result, nil
		}
		return nil, errAIIntent
	}
	if !errors.Is(lookup, pgx.ErrNoRows) {
		return nil, lookup
	}
	if custom == nil && (configurationErr != nil || !cfg.Enabled) {
		return nil, ai.ErrProviderDisabled
	}
	if !cfg.Enabled {
		return nil, ai.ErrProviderDisabled
	}
	if targetDigest != "" && targetDigest != cfg.ConfigurationDigest {
		return nil, errAITargetChanged
	}
	if custom == nil && cfg.Provider != "ollama" {
		if targetDigest == "" || cfg.CloudScopeHash != AICloudScopeHash {
			return nil, errAIConsent
		}
		allowed, consentErr := q.AICloudAuthorized(ctx, store.AICloudAuthorizedParams{TenantID: principal.TenantID, ConfigurationDigest: cfg.ConfigurationDigest, ScopeHash: AICloudScopeHash})
		if consentErr != nil {
			return nil, consentErr
		}
		if !allowed {
			return nil, errAIConsent
		}
	}
	if cfg.LocalOnly && cfg.Provider != "ollama" {
		return nil, ai.ErrLocalOnly
	}
	if (cfg.Mode == "local") != (cfg.Provider == "ollama") {
		return nil, ai.ErrProviderDisabled
	}
	budgetNow := time.Now().Truncate(time.Microsecond)
	quota, err := resolveAIQuota(ctx, q, cfg, principal.TenantID, budgetNow)
	if err != nil {
		if custom != nil {
			quota = aiQuotaResolution{
				Source:      "personal_byok",
				Independent: true,
				Start:       pgtype.Timestamptz{Time: budgetNow.Add(-time.Minute), Valid: true},
				End:         pgtype.Timestamptz{Time: budgetNow.Add(time.Minute), Valid: true},
			}
			quota.Limits.AI.Calls = 60
			quota.Limits.AI.MailboxCalls = 60
			quota.Limits.AI.Concurrency = 5
			quota.Limits.AI.Tokens = math.MaxInt64
			quota.Limits.AI.Cost = math.MaxInt64
		} else {
			return nil, errAIBudget
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
	reservedTokens := int64(len(raw)) + 4096 // UTF-8 byte bound plus system prompt/output headroom.
	tokenLimit, costLimit := limits.AI.Tokens, limits.AI.Cost
	reservedCost := int64(0)
	if cfg.Mode == "platform" {
		// A tenant without a configured currency bills in the platform default.
		currency := cfg.CostCurrency
		if currency == "" {
			currency = "CNY"
		}
		if tokenLimit <= 0 || costLimit <= 0 || limits.AI.Currency != currency || (currency != "CNY" && currency != "USD") || (cfg.InputMicrosPerMillion == 0 && cfg.OutputMicrosPerMillion == 0) {
			return nil, errAIBudget
		}
		reservedCost, err = aiCost(reservedTokens, 2048, cfg.InputMicrosPerMillion, cfg.OutputMicrosPerMillion)
		if err != nil {
			return nil, err
		}
	} else {
		tokenLimit, costLimit = math.MaxInt64, math.MaxInt64
	}
	if custom == nil {
		if err = checkAIProviderBudget(ctx, q, cfg, principal.TenantID, reservedTokens, reservedCost, budgetNow); err != nil {
			if errors.Is(err, ErrAIBudgetExceeded) {
				return nil, errAIBudget
			}
			return nil, err
		}
	}
	var provider ai.Provider
	switch {
	case s.Provider != nil:
		provider, err = s.Provider(cfg)
	case custom != nil:
		if cfg.Provider == ai.ProviderOllama {
			provider, err = ai.ConfiguredProvider(cfg.Provider, cfg.Endpoint, cfg.Model, "", nil, s.LocalEndpoints)
		} else {
			provider, err = ai.DynamicProvider(cfg.Provider, cfg.Endpoint, cfg.Model, custom.APIKey)
		}
	default:
		credential := ""
		if cfg.Provider != "ollama" {
			if s.Secrets == nil {
				return nil, ai.ErrProviderDisabled
			}
			credential, err = s.Secrets.DecryptString(cfg.EncryptedKey)
			if err != nil {
				return nil, ai.ErrProviderDisabled
			}
		}
		provider, err = ai.ConfiguredProvider(cfg.Provider, cfg.Endpoint, cfg.Model, credential, s.CloudEndpoints, s.LocalEndpoints)
	}
	if err != nil {
		return nil, err
	}
	reservation, err := q.PlatformReserveAIRequest(ctx, store.PlatformReserveAIRequestParams{TenantID: principal.TenantID, MailboxID: principal.MailboxID, RequestKey: key, InputDigest: hash, AccountedAt: pgtype.Timestamptz{Time: budgetNow, Valid: true}, IndependentWindow: quota.Independent, PeriodStart: start, PeriodEnd: end, Mode: cfg.Mode, Provider: cfg.Provider, Model: cfg.Model, CostCurrency: cfg.CostCurrency, InputRate: cfg.InputMicrosPerMillion, OutputRate: cfg.OutputMicrosPerMillion, ReservedTokens: reservedTokens, ReservedCostMicros: reservedCost, CallLimit: limits.AI.Calls, MailboxCallLimit: limits.AI.MailboxCalls, ConcurrencyLimit: limits.AI.Concurrency, TokenLimit: tokenLimit, CostLimit: costLimit})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errAIBudget
	}
	if err != nil {
		return nil, err
	}
	if tx.Commit(ctx) != nil {
		return nil, errAIBudget
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request.Model = cfg.Model
	completion, callErr := provider.Complete(callCtx, request)
	status := "uncertain"
	var actualTokens, actualCost pgtype.Int8
	var result json.RawMessage
	if callErr == nil {
		result, callErr = ai.ValidateOperation(input.Operation, completion.Content)
		// Unknown usage remains charged at its reservation even with a valid result.
		if completion.UsageKnown && completion.PromptTokens >= 0 && completion.OutputTokens >= 0 {
			tokens := int64(completion.PromptTokens) + int64(completion.OutputTokens)
			cost := int64(0)
			if cfg.Mode == "platform" {
				cost, err = aiCost(int64(completion.PromptTokens), int64(completion.OutputTokens), cfg.InputMicrosPerMillion, cfg.OutputMicrosPerMillion)
			}
			if err == nil {
				actualTokens = pgtype.Int8{Int64: tokens, Valid: true}
				actualCost = pgtype.Int8{Int64: cost, Valid: true}
				if callErr == nil && tokens <= reservedTokens && cost <= reservedCost {
					status = "completed"
				}
			}
		} else if cfg.Mode != "platform" && callErr == nil {
			status = "completed"
		}
	}
	settleCtx, settleCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer settleCancel()
	finished, finishErr := store.New(s.Pool).PlatformFinishAIRequest(settleCtx, store.PlatformFinishAIRequestParams{TenantID: principal.TenantID, ID: reservation.ID, Status: status, ActualTokens: actualTokens, ActualCostMicros: actualCost, Result: result})
	if finishErr != nil || finished != 1 {
		return nil, errAIBudget
	}
	if callErr != nil {
		return nil, ai.ErrProviderError
	}
	if status != "completed" {
		return nil, errAIBudget
	}
	return result, nil
}

// AIConfigurationHandler exposes only redacted configuration and usage to panel administrators.
type AIConfigurationHandler struct {
	HealthRepository               AIHealthRepository
	FetchHealthModels              func(context.Context, ai.Provider) ([]string, error)
	Pool                           *db.Pool
	Secrets                        SecretsBox
	CloudEndpoints, LocalEndpoints []string
}

// Get returns redacted provider configuration.
func (h *AIConfigurationHandler) Get(c *gin.Context) {
	if h.Pool == nil {
		authError(c, 503, "AI_DISABLED")
		return
	}
	tid, ok := middleware.GetTenantID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	cfg, err := store.New(h.Pool).PlatformGetAIProvider(c, tid)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(200, gin.H{"configuration": gin.H{"enabled": false, "mode": "local", "provider": "ollama", "local_only": true}, "cloud_endpoints": safelyListedEndpoints(h.CloudEndpoints), "local_endpoints": safelyListedEndpoints(h.LocalEndpoints)})
		return
	}
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	configuration := gin.H{"mode": cfg.Mode, "provider": cfg.Provider, "endpoint": cfg.Endpoint, "model": cfg.Model, "enabled": cfg.Enabled, "local_only": cfg.LocalOnly, "credential_configured": cfg.EncryptedKey != ""}
	for key, value := range aiConfigurationQuotaFields(cfg) {
		configuration[key] = value
	}
	policy, policyErr := aiTargetPolicy(c, store.New(h.Pool), cfg)
	if policyErr != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	c.JSON(200, gin.H{"configuration": configuration, "consent": policy, "cloud_endpoints": safelyListedEndpoints(h.CloudEndpoints), "local_endpoints": safelyListedEndpoints(h.LocalEndpoints)})
}

// Put updates a tenant-owned provider through the scoped DB function.
func (h *AIConfigurationHandler) Put(c *gin.Context) {
	if h.Pool == nil {
		authError(c, 503, "AI_DISABLED")
		return
	}
	tid, ok := middleware.GetTenantID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	var input struct {
		Mode              string   `json:"mode"`
		Provider          string   `json:"provider"`
		Endpoint          string   `json:"endpoint"`
		Model             string   `json:"model"`
		Key               string   `json:"api_key"`
		Enabled           bool     `json:"enabled"`
		LocalOnly         bool     `json:"local_only"`
		PersonalMode      string   `json:"personal_mode"`
		ApprovedEndpoints []string `json:"approved_endpoints"`
		ActiveService     string   `json:"active_service"`
	}
	if c.ShouldBindJSON(&input) != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if input.PersonalMode != "" && input.PersonalMode != "disabled" && input.PersonalMode != "approved" && input.PersonalMode != "public" {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if input.ActiveService != "" && input.ActiveService != "platform" && input.ActiveService != "enterprise" {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	raw, ok := middleware.QueriesFromCtx(c)
	tenantQueries, scoped := raw.(*store.Queries)
	if !ok || !scoped {
		authError(c, 503, "DB_ERROR")
		return
	}
	if input.Mode == "" && (input.PersonalMode != "" || input.ApprovedEndpoints != nil || input.ActiveService != "") {
		if input.PersonalMode != "" || input.ApprovedEndpoints != nil {
			pm := input.PersonalMode
			if pm == "" {
				pm = "public"
			}
			eps := input.ApprovedEndpoints
			if eps == nil {
				eps = []string{}
			}
			if err := tenantQueries.PlatformConfigureTenantAIGovernance(c, store.PlatformConfigureTenantAIGovernanceParams{PersonalMode: pm, ApprovedEndpoints: eps}); err != nil {
				authError(c, 503, "DB_ERROR")
				return
			}
		}
		if input.ActiveService != "" {
			if err := tenantQueries.PlatformSetActiveAIService(c, store.PlatformSetActiveAIServiceParams{TenantID: tid, ActiveService: input.ActiveService}); err != nil {
				authError(c, 503, "DB_ERROR")
				return
			}
		}
		c.Status(http.StatusNoContent)
		return
	}
	if len(input.Model) == 0 || len(input.Model) > 200 || len(input.Key) > 4096 || (input.Mode != "byo" && input.Mode != "local") {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if input.LocalOnly && input.Provider != "ollama" {
		authError(c, 400, "AI_LOCAL_ONLY")
		return
	}
	if (input.Mode == "local") != (input.Provider == "ollama") {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if input.Provider == "ollama" && input.Key != "" {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if h.Secrets == nil && input.Provider != "ollama" {
		authError(c, 503, "AI_DISABLED")
		return
	}
	q := store.New(h.Pool)
	encrypted := ""
	if input.Key != "" {
		var err error
		encrypted, err = h.Secrets.EncryptString(input.Key)
		if err != nil {
			authError(c, 503, "AI_DISABLED")
			return
		}
	} else if previous, err := q.PlatformGetAIProvider(c, tid); err == nil {
		if (previous.EnterpriseProvider == input.Provider || previous.Provider == input.Provider) && (strings.TrimRight(previous.EnterpriseEndpoint, "/") == strings.TrimRight(input.Endpoint, "/") || strings.TrimRight(previous.Endpoint, "/") == strings.TrimRight(input.Endpoint, "/")) {
			if previous.EnterpriseEncryptedKey != "" {
				encrypted = previous.EnterpriseEncryptedKey
			} else {
				encrypted = previous.EncryptedKey
			}
		}
	}
	if input.Enabled {
		credential := input.Key
		if credential == "" && encrypted != "" {
			credential, _ = h.Secrets.DecryptString(encrypted)
		}
		if _, err := ai.ConfiguredProvider(input.Provider, input.Endpoint, input.Model, credential, h.CloudEndpoints, h.LocalEndpoints); err != nil {
			authError(c, 400, "AI_ENDPOINT_FORBIDDEN")
			return
		}
	}
	err := tenantQueries.PlatformConfigureTenantAI(c, store.PlatformConfigureTenantAIParams{Mode: input.Mode, Provider: input.Provider, Endpoint: strings.TrimRight(input.Endpoint, "/"), Model: input.Model, EncryptedKey: encrypted, Enabled: input.Enabled, LocalOnly: input.LocalOnly})
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	if input.PersonalMode != "" || input.ApprovedEndpoints != nil {
		pm := input.PersonalMode
		if pm == "" {
			pm = "public"
		}
		eps := input.ApprovedEndpoints
		if eps == nil {
			eps = []string{}
		}
		if err := tenantQueries.PlatformConfigureTenantAIGovernance(c, store.PlatformConfigureTenantAIGovernanceParams{PersonalMode: pm, ApprovedEndpoints: eps}); err != nil {
			authError(c, 503, "DB_ERROR")
			return
		}
	}
	if input.ActiveService != "" {
		if err := tenantQueries.PlatformSetActiveAIService(c, store.PlatformSetActiveAIServiceParams{TenantID: tid, ActiveService: input.ActiveService}); err != nil {
			authError(c, 503, "DB_ERROR")
			return
		}
	}
	c.Status(http.StatusNoContent)
}

// Usage lists accounting metadata without email or model output.
func (h *AIConfigurationHandler) Usage(c *gin.Context) {
	if h.Pool == nil {
		authError(c, 503, "AI_DISABLED")
		return
	}
	tid, ok := middleware.GetTenantID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	rows, err := store.New(h.Pool).PlatformAIUsage(c, tid)
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	metadata := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		item := aiTokenMetadata(row.ReservedTokens, row.ActualTokens)
		item["id"] = row.ID
		item["mailbox_id"] = row.MailboxID
		item["request_key"] = row.RequestKey
		item["period_start"] = row.PeriodStart
		item["period_end"] = row.PeriodEnd
		item["mode"] = row.Mode
		item["provider"] = row.Provider
		item["model"] = row.Model
		item["cost_currency"] = row.CostCurrency
		item["status"] = row.Status
		item["reserved_cost_micros"] = row.ReservedCostMicros
		item["actual_cost_micros"] = row.ActualCostMicros
		item["created_at"] = row.CreatedAt
		metadata = append(metadata, item)
	}
	c.JSON(200, gin.H{"requests": metadata})
}

// PlatformPut configures platform-funded models and explicit accounting rates.
func (h *AIConfigurationHandler) PlatformPut(c *gin.Context) {
	tid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || tid <= 0 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	var input struct {
		Provider   string `json:"provider"`
		Endpoint   string `json:"endpoint"`
		Model      string `json:"model"`
		Key        string `json:"api_key"`
		Enabled    bool   `json:"enabled"`
		InputRate  int64  `json:"input_micros_per_million"`
		OutputRate int64  `json:"output_micros_per_million"`
		Currency   string `json:"cost_currency"`
	}
	if h.Pool == nil || h.Secrets == nil {
		authError(c, 503, "AI_DISABLED")
		return
	}
	if c.ShouldBindJSON(&input) != nil || len(input.Model) == 0 || len(input.Model) > 200 || len(input.Key) > 4096 || input.InputRate < 0 || input.OutputRate < 0 || input.InputRate > 9007199254740991 || input.OutputRate > 9007199254740991 || (input.Currency != "CNY" && input.Currency != "USD") || (input.Enabled && input.InputRate == 0 && input.OutputRate == 0) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if input.Provider != "openai" && input.Provider != "azure" {
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
	encrypted := ""
	credential := input.Key
	if credential != "" {
		encrypted, err = h.Secrets.EncryptString(credential)
	} else if previous, lookup := q.PlatformGetAIProvider(c, tid); lookup == nil && previous.Mode == "platform" && previous.Provider == input.Provider && previous.Endpoint == strings.TrimRight(input.Endpoint, "/") {
		encrypted = previous.EncryptedKey
		credential, err = h.Secrets.DecryptString(encrypted)
	}
	if err != nil {
		authError(c, 503, "AI_DISABLED")
		return
	}
	if input.Enabled {
		if _, err = ai.ConfiguredProvider(input.Provider, input.Endpoint, input.Model, credential, h.CloudEndpoints, h.LocalEndpoints); err != nil {
			authError(c, 400, "AI_ENDPOINT_FORBIDDEN")
			return
		}
	}
	if _, err = q.PlatformSetAIProvider(c, store.PlatformSetAIProviderParams{TenantID: tid, Mode: "platform", Provider: input.Provider, Endpoint: strings.TrimRight(input.Endpoint, "/"), Model: input.Model, EncryptedKey: encrypted, Enabled: input.Enabled, InputMicrosPerMillion: input.InputRate, OutputMicrosPerMillion: input.OutputRate, CostCurrency: input.Currency}); err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	uid, ok := middleware.GetUserID(c)
	detail, _ := json.Marshal(gin.H{"status": 200, "outcome": "succeeded", "provider": input.Provider, "enabled": input.Enabled})
	if _, err = q.CreateAuditLog(c, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: tid, Valid: true}, ActorUserID: pgtype.Int8{Int64: uid, Valid: ok}, Action: "ai.platform.configure", TargetType: pgtype.Text{String: "tenant", Valid: true}, Detail: detail}); err != nil {
		authError(c, 503, "AUDIT_UNAVAILABLE")
		return
	}
	if tx.Commit(c) != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	c.Status(204)
}

func safelyListedEndpoints(endpoints []string) []string {
	safe := []string{}
	for _, endpoint := range endpoints {
		parsed, err := url.Parse(endpoint)
		if err == nil && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && (parsed.Scheme == "http" || parsed.Scheme == "https") {
			safe = append(safe, endpoint)
		}
	}
	return safe
}

// AIHealthHandler has no Pool, Store, or unrestricted query capability.
// Accidental platform-pool access in this diagnostic is a compile error.
type AIHealthHandler struct {
	HealthRepository               AIHealthRepository
	Secrets                        SecretsBox
	CloudEndpoints, LocalEndpoints []string
	FetchHealthModels              func(context.Context, ai.Provider) ([]string, error)
}

// Health forwards only the diagnostic capability, never the configuration pool.
func (h *AIConfigurationHandler) Health(c *gin.Context) {
	diagnostic := AIHealthHandler{HealthRepository: h.HealthRepository, Secrets: h.Secrets, CloudEndpoints: h.CloudEndpoints, LocalEndpoints: h.LocalEndpoints, FetchHealthModels: h.FetchHealthModels}
	diagnostic.Health(c)
}

// Health performs an explicit, bounded metadata-only provider diagnostic.
func (h *AIHealthHandler) Health(c *gin.Context) {
	if h.HealthRepository == nil {
		authError(c, 503, "AI_DISABLED")
		return
	}
	tid, ok := middleware.GetTenantID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	if _, active := middleware.QueriesFromCtx(c); active {
		authError(c, 503, "PROBE_TRANSACTION_ACTIVE")
		return
	}
	uid, _ := middleware.GetUserID(c)
	actor := ProbeActor{TenantID: tid, UserID: uid}
	snapshot, snapshotErr := h.HealthRepository.ReadHealth(c.Request.Context(), actor)
	if snapshotErr != nil {
		respondProbeError(c, snapshotErr)
		return
	}
	var input struct {
		Provider string `json:"provider"`
		Endpoint string `json:"endpoint"`
		Model    string `json:"model"`
		APIKey   string `json:"api_key"`
	}
	_ = c.ShouldBindJSON(&input)
	if input.Endpoint != "" {
		if input.Provider == "" {
			input.Provider = "openai"
		}
		if input.Model == "" {
			input.Model = "gpt-4o"
		}
		// F04: Stored key reuse when endpoint and provider match stored config
		if input.APIKey == "" && input.Provider != "ollama" {
			stored := snapshot.Configuration
			if snapshot.Present && h.Secrets != nil {
				keyEnc := stored.EncryptedKey
				if stored.EnterpriseEncryptedKey != "" && strings.TrimRight(stored.EnterpriseEndpoint, "/") == strings.TrimRight(input.Endpoint, "/") {
					keyEnc = stored.EnterpriseEncryptedKey
				}
				if (stored.Provider == input.Provider || stored.EnterpriseProvider == input.Provider) &&
					(strings.TrimRight(stored.Endpoint, "/") == strings.TrimRight(input.Endpoint, "/") || strings.TrimRight(stored.EnterpriseEndpoint, "/") == strings.TrimRight(input.Endpoint, "/")) &&
					keyEnc != "" {
					decrypted, decErr := h.Secrets.DecryptString(keyEnc)
					if decErr == nil {
						input.APIKey = decrypted
					}
				}
			}
		}
		// F02/F03/D19: Endpoint security: Candidate health test must NOT bypass local endpoint / SSRF / loopback checks.
		// Local endpoints are restricted to platform approved list.
		var candidate ai.Provider
		var err error
		if input.Provider == "ollama" {
			candidate, err = ai.ConfiguredProvider(input.Provider, input.Endpoint, input.Model, "", nil, h.LocalEndpoints)
		} else {
			candidate, err = ai.DynamicProvider(input.Provider, input.Endpoint, input.Model, input.APIKey)
		}
		if err != nil {
			if errors.Is(err, ai.ErrProviderDisabled) || err.Error() == ai.ErrProviderDisabled.Error() {
				authError(c, 403, "AI_ENDPOINT_FORBIDDEN")
				return
			}
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		models, err := h.fetchHealth(ctx, candidate)
		outcome := "reachable"
		if err != nil {
			outcome = "unreachable"
		}
		if completeErr := h.HealthRepository.CompleteHealth(c.Request.Context(), actor, snapshot, true, outcome); completeErr != nil {
			respondProbeError(c, completeErr)
			return
		}
		if err != nil {
			authError(c, 502, "AI_PROVIDER_UNREACHABLE")
			return
		}
		c.JSON(200, gin.H{"status": "reachable", "models": models, "operation": "models_metadata", "completion_performed": false})
		return
	}
	cfg := snapshot.Configuration
	if !snapshot.Present || !cfg.Enabled {
		authError(c, 409, "AI_DISABLED")
		return
	}
	if cfg.LocalOnly && cfg.Provider != "ollama" {
		authError(c, 403, "AI_LOCAL_ONLY")
		return
	}
	if cfg.Provider != "ollama" {
		if !snapshot.CloudAuthorized {
			authError(c, 403, "AI_CLOUD_CONSENT_REQUIRED")
			return
		}
	}
	key := ""
	var err error
	if cfg.Provider != "ollama" {
		if h.Secrets == nil {
			authError(c, 503, "AI_DISABLED")
			return
		}
		key, err = h.Secrets.DecryptString(cfg.EncryptedKey)
		if err != nil {
			authError(c, 503, "AI_DISABLED")
			return
		}
	}
	provider, err := ai.ConfiguredProvider(cfg.Provider, cfg.Endpoint, cfg.Model, key, h.CloudEndpoints, h.LocalEndpoints)
	if err != nil {
		authError(c, 403, "AI_ENDPOINT_FORBIDDEN")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	models, err := h.fetchHealth(ctx, provider)
	outcome := "reachable"
	if err != nil {
		outcome = "unreachable"
	}
	if completeErr := h.HealthRepository.CompleteHealth(c.Request.Context(), actor, snapshot, false, outcome); completeErr != nil {
		respondProbeError(c, completeErr)
		return
	}
	if err != nil {
		authError(c, 502, "AI_PROVIDER_UNREACHABLE")
		return
	}
	c.JSON(200, gin.H{"status": "reachable", "models": models, "operation": "models_metadata", "completion_performed": false})
}

// ResolveUsage requires a platform operator to supply explicit observed usage;
// missing usage never releases a reservation automatically.
func (h *AIConfigurationHandler) ResolveUsage(c *gin.Context) {
	if h.Pool == nil {
		authError(c, 503, "AI_DISABLED")
		return
	}
	tid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	id, idErr := strconv.ParseInt(c.Param("request_id"), 10, 64)
	var input struct {
		Status   string `json:"status"`
		Tokens   *int64 `json:"tokens"`
		Cost     *int64 `json:"cost_micros"`
		Currency string `json:"cost_currency"`
		Reason   string `json:"reason"`
	}
	if err != nil || idErr != nil || tid <= 0 || id <= 0 || c.ShouldBindJSON(&input) != nil || input.Tokens == nil || input.Cost == nil || *input.Tokens < 0 || *input.Cost < 0 || *input.Tokens > 9007199254740991 || *input.Cost > 9007199254740991 || len(strings.TrimSpace(input.Reason)) < 10 || len(input.Reason) > 2000 || (input.Status != "resolved" && input.Status != "released") || (input.Status == "released" && (*input.Tokens != 0 || *input.Cost != 0)) || (input.Currency != "CNY" && input.Currency != "USD") {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	uid, ok := middleware.GetUserID(c)
	if !ok {
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
	if err = q.PlatformLockBillingTenant(c, tid); err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	if err = q.PlatformExpireAIReservations(c, tid); err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	row, err := q.PlatformResolveAIUsage(c, store.PlatformResolveAIUsageParams{TenantID: tid, ID: id, Status: input.Status, ActualTokens: pgtype.Int8{Int64: *input.Tokens, Valid: true}, ActualCostMicros: pgtype.Int8{Int64: *input.Cost, Valid: true}, ReviewReason: pgtype.Text{String: strings.TrimSpace(input.Reason), Valid: true}, ReviewedBy: pgtype.Int8{Int64: uid, Valid: true}, CostCurrency: input.Currency})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			authError(c, 409, "AI_REVIEW_CONFLICT")
		} else {
			authError(c, 503, "DB_ERROR")
		}
		return
	}
	if row.Mode != "platform" && *input.Cost != 0 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	detail, _ := json.Marshal(gin.H{"status": 200, "outcome": "succeeded", "request_id": id, "resolution": input.Status, "tokens": *input.Tokens, "cost_micros": *input.Cost, "cost_currency": input.Currency, "reason": strings.TrimSpace(input.Reason)})
	if _, err = q.CreateAuditLog(c, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: tid, Valid: true}, ActorUserID: pgtype.Int8{Int64: uid, Valid: true}, Action: "ai.usage.reviewed", TargetType: pgtype.Text{String: "ai_request", Valid: true}, Detail: detail}); err != nil {
		authError(c, 503, "AUDIT_UNAVAILABLE")
		return
	}
	if tx.Commit(c) != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	c.JSON(200, gin.H{"request": gin.H{"id": row.ID, "status": row.Status, "actual_tokens": row.ActualTokens, "actual_tokens_exact": strconv.FormatInt(*input.Tokens, 10), "actual_cost_micros": row.ActualCostMicros, "cost_currency": input.Currency, "reviewed_by": uid, "review_reason": strings.TrimSpace(input.Reason)}})
}

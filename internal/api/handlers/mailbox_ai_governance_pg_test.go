package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/ai"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func newTestSecrets(t *testing.T) SecretsBox {
	t.Helper()
	box, err := secrets.New(bytes.Repeat([]byte{41}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

// TestMailboxAIIndependentStorageAndSwitching tests D01:
// Platform vs Enterprise configurations stored independently.
// Switching services does not overwrite configs or credentials.
func TestMailboxAIIndependentStorageAndSwitching(t *testing.T) {
	pool, principal := mailboxAIFixture(t, 100)
	sec := newTestSecrets(t)

	h := &AIConfigurationHandler{
		Pool:           pool,
		Secrets:        sec,
		CloudEndpoints: []string{"https://api.openai.com/v1", "https://platform.openai.test/v1"},
		LocalEndpoints: []string{"http://127.0.0.1:11434"},
	}

	// 1. Configure Platform AI
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	c.Request = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/platform/tenants/1/ai/configuration", strings.NewReader(`{
		"provider": "openai",
		"endpoint": "https://platform.openai.test/v1",
		"model": "platform-gpt",
		"api_key": "platform-secret-key",
		"enabled": true,
		"input_micros_per_million": 1000,
		"output_micros_per_million": 2000,
		"cost_currency": "CNY"
	}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.PlatformPut(c)
	if w.Code != 200 && w.Code != 204 {
		t.Fatalf("PlatformPut failed: %d %s", w.Code, w.Body.String())
	}

	// Verify Platform AI stored
	var ownerID int64
	if err := pool.QueryRow(t.Context(), "SELECT user_id FROM user_tenant_roles WHERE tenant_id=$1 AND role='owner' LIMIT 1", principal.TenantID).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	q := store.New(pool)
	cfg, err := q.PlatformGetAIProvider(t.Context(), principal.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PlatformModel != "platform-gpt" || cfg.PlatformEndpoint != "https://platform.openai.test/v1" || !cfg.PlatformEnabled {
		t.Fatalf("unexpected platform config: %+v", cfg)
	}

	// 2. Configure Enterprise AI
	authRouter := gin.New()
	authRouter.Use(func(ctx *gin.Context) {
		tx, err := pool.UserTx(ctx.Request.Context(), principal.TenantID, ownerID)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		tenantQ := store.New(tx)
		ctx.Set(middleware.ContextTenantID, principal.TenantID)
		ctx.Set(middleware.ContextUserID, ownerID)
		ctx.Set(middleware.ContextQueries, tenantQ)
		ctx.Next()
		_ = tx.Commit(context.Background())
	})
	authRouter.PUT("/api/v1/ai/configuration", h.Put)
	authRouter.GET("/api/v1/ai/configuration", h.Get)

	entBody := `{
		"mode": "byo",
		"provider": "openai",
		"endpoint": "https://api.openai.com/v1",
		"model": "enterprise-model",
		"api_key": "enterprise-secret-key",
		"enabled": true,
		"local_only": false
	}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/v1/ai/configuration", strings.NewReader(entBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	authRouter.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatalf("Enterprise Put failed: %d %s", w.Code, w.Body.String())
	}

	// Verify both configs exist and platform config was NOT overwritten
	cfg, err = q.PlatformGetAIProvider(t.Context(), principal.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PlatformModel != "platform-gpt" || cfg.PlatformEndpoint != "https://platform.openai.test/v1" {
		t.Fatalf("Platform config was overwritten: %+v", cfg)
	}
	if cfg.EnterpriseModel != "enterprise-model" || cfg.EnterpriseEndpoint != "https://api.openai.com/v1" {
		t.Fatalf("Enterprise config not stored properly: %+v", cfg)
	}

	// Decrypt keys and verify they are distinct
	platKey, err := sec.DecryptString(cfg.PlatformEncryptedKey)
	if err != nil || platKey != "platform-secret-key" {
		t.Fatalf("Platform key corrupted: %v %s", err, platKey)
	}
	entKey, err := sec.DecryptString(cfg.EnterpriseEncryptedKey)
	if err != nil || entKey != "enterprise-secret-key" {
		t.Fatalf("Enterprise key corrupted: %v %s", err, entKey)
	}

	// 3. Switch active service to platform
	req = httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/v1/ai/configuration", strings.NewReader(`{"active_service":"platform"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	authRouter.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatalf("Switch to platform failed: %d %s", w.Code, w.Body.String())
	}

	cfg, _ = q.PlatformGetAIProvider(t.Context(), principal.TenantID)
	if cfg.ActiveService != "platform" || cfg.Model != "platform-gpt" {
		t.Fatalf("Active service did not switch to platform: %+v", cfg)
	}
	// Enterprise key remains intact
	entKey, _ = sec.DecryptString(cfg.EnterpriseEncryptedKey)
	if entKey != "enterprise-secret-key" {
		t.Fatalf("Enterprise key lost after switch: %s", entKey)
	}
}

// TestMailboxAIPersonalBYOKGovernance tests D02:
// personal_mode IN ('disabled', 'approved', 'public')
// - disabled: rejected (403 AI_PERSONAL_BYOK_DISABLED)
// - approved: only approved endpoints allowed
// - public: public HTTPS allowed
func TestMailboxAIPersonalBYOKGovernance(t *testing.T) {
	pool, principal := mailboxAIFixture(t, 100)
	var ownerID int64
	if err := pool.QueryRow(t.Context(), "SELECT user_id FROM user_tenant_roles WHERE tenant_id=$1 AND role='owner' LIMIT 1", principal.TenantID).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	_ = store.New(pool)
	sec := newTestSecrets(t)

	h := &AIConfigurationHandler{
		Pool:           pool,
		Secrets:        sec,
		CloudEndpoints: []string{"https://api.openai.com/v1"},
		LocalEndpoints: []string{"http://127.0.0.1:11434"},
	}
	authRouter := gin.New()
	authRouter.Use(func(ctx *gin.Context) {
		tx, err := pool.UserTx(ctx.Request.Context(), principal.TenantID, ownerID)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		tenantQ := store.New(tx)
		ctx.Set(middleware.ContextTenantID, principal.TenantID)
		ctx.Set(middleware.ContextUserID, ownerID)
		ctx.Set(middleware.ContextQueries, tenantQ)
		ctx.Next()
		_ = tx.Commit(context.Background())
	})
	authRouter.PUT("/api/v1/ai/configuration", h.Put)

	nativeH := &NativeMailboxAI{
		Service: &MailboxAIService{
			Pool:           pool,
			Secrets:        sec,
			LocalEndpoints: []string{"http://127.0.0.1:11434"},
		},
	}

	userTarget := customQuotaTarget(t, pool, principal, "openai")
	sess := webmail.Session{TenantID: principal.TenantID, MailboxID: principal.MailboxID}

	// Case 1: personal_mode = 'disabled' -> 403 AI_PERSONAL_BYOK_DISABLED
	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/v1/ai/configuration", strings.NewReader(`{"personal_mode":"disabled"}`))
	req.Header.Set("Content-Type", "application/json")
	authRouter.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatalf("set personal_mode disabled failed: %d %s", w.Code, w.Body.String())
	}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ai/target", nil)
	allowed := nativeH.customGovernance(c, sess, userTarget)
	if allowed {
		t.Fatal("expected customGovernance to reject disabled personal BYOK")
	}

	// Verify RunCustom also rejects with AI_PERSONAL_BYOK_DISABLED
	service := &MailboxAIService{Pool: pool, Secrets: sec}
	input := ai.OperationInput{Operation: "summarize", Body: "test"}
	_, err := service.RunCustom(t.Context(), principal, "byok-gov-disabled-key", input, userTarget, userTarget.Digest)
	if err == nil || !strings.Contains(err.Error(), "AI_PERSONAL_BYOK_DISABLED") {
		t.Fatalf("expected ErrAIPersonalBYOKDisabled, got: %v", err)
	}

	// Case 2: personal_mode = 'approved' -> only approved endpoints allowed
	w = httptest.NewRecorder()
	req = httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/v1/ai/configuration", strings.NewReader(`{
		"personal_mode": "approved",
		"approved_endpoints": ["https://approved.company.test/v1"]
	}`))
	req.Header.Set("Content-Type", "application/json")
	authRouter.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatalf("set personal_mode approved failed: %d %s", w.Code, w.Body.String())
	}

	// userTarget endpoint is "https://byok.example.test/v1" -> NOT approved
	c, _ = gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ai/target", nil)
	if nativeH.customGovernance(c, sess, userTarget) {
		t.Fatal("expected unapproved endpoint to be rejected")
	}

	// Approve userTarget endpoint
	w = httptest.NewRecorder()
	req = httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/v1/ai/configuration", strings.NewReader(`{
		"personal_mode": "approved",
		"approved_endpoints": ["https://byok.example.test/v1"]
	}`))
	req.Header.Set("Content-Type", "application/json")
	authRouter.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatalf("approve endpoint failed: %d", w.Code)
	}

	c, _ = gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ai/target", nil)
	if !nativeH.customGovernance(c, sess, userTarget) {
		t.Fatal("expected approved endpoint to be allowed")
	}

	// Case 3: personal_mode = 'public' -> public HTTPS allowed
	w = httptest.NewRecorder()
	req = httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/v1/ai/configuration", strings.NewReader(`{"personal_mode":"public"}`))
	req.Header.Set("Content-Type", "application/json")
	authRouter.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatalf("set personal_mode public failed: %d", w.Code)
	}

	c, _ = gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ai/target", nil)
	if !nativeH.customGovernance(c, sess, userTarget) {
		t.Fatal("expected public HTTPS endpoint to be allowed in public mode")
	}
}

// TestMailboxAIPersonalBYOKWithNoEnterpriseModel tests D03 & D04:
// If enterprise has no default model configured or enabled, personal BYOK CAN STILL BE USED.
// Personal BYOK has NO daily/monthly token/cost business quotas, retains technical limits.
func TestMailboxAIPersonalBYOKWithNoEnterpriseModel(t *testing.T) {
	pool, principal := mailboxAIFixture(t, 0) // 0 calls entitlement in plan!
	sec := newTestSecrets(t)

	// Explicitly disable enterprise provider
	_, err := pool.Exec(t.Context(), "UPDATE ai_provider_configs SET enabled=false, daily_token_limit=1 WHERE tenant_id=$1", principal.TenantID)
	if err != nil {
		t.Fatal(err)
	}

	target := customQuotaTarget(t, pool, principal, "openai")
	model := &countedAI{}
	service := &MailboxAIService{
		Pool:    pool,
		Secrets: sec,
		Provider: func(cfg store.AiProviderConfigs) (ai.Provider, error) {
			return model, nil
		},
	}
	input := ai.OperationInput{Operation: "summarize", Body: "personal byok request"}

	// Even though enterprise enabled=false and daily_token_limit=1 (which would exceed 0 or 1 token),
	// personal BYOK succeeds because it has NO business token/cost quotas and works without enterprise default model!
	res, err := service.RunCustom(t.Context(), principal, "byok-d03-d04-request-key", input, target, target.Digest)
	if err != nil {
		t.Fatalf("personal BYOK failed when enterprise model disabled: %v", err)
	}
	if len(res) == 0 {
		t.Fatal("empty response from custom AI")
	}
	if model.calls.Load() != 1 {
		t.Fatalf("expected 1 model call, got %d", model.calls.Load())
	}
}

// TestMailboxAIHealthSecurityAndKeyReuse tests F02, F03, F04, D19:
// - Local endpoints tested via candidate health test must be in platform approved list.
// - Stored key reuse: if endpoint and provider match, reuse stored key without re-entering.
// - If endpoint changed, refuse to reuse stored key.
func TestMailboxAIHealthSecurityAndKeyReuse(t *testing.T) {
	pool, principal := mailboxAIFixture(t, 100)
	sec := newTestSecrets(t)

	h := &AIConfigurationHandler{
		Pool:           pool,
		Secrets:        sec,
		CloudEndpoints: []string{"https://api.openai.com/v1"},
		LocalEndpoints: []string{"http://127.0.0.1:11434"},
	}

	authRouter := gin.New()
	authRouter.Use(func(ctx *gin.Context) {
		ctx.Set("urmail.tenant_id", principal.TenantID)
		ctx.Next()
	})
	authRouter.POST("/api/v1/ai/health", h.Health)

	// F02/F03/D19: Loopback endpoint NOT in LocalEndpoints must be rejected
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/ai/health", strings.NewReader(`{
		"provider": "ollama",
		"endpoint": "http://127.0.0.1:9999",
		"model": "llama3"
	}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	authRouter.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("expected 403 for unapproved local endpoint, got %d %s", w.Code, w.Body.String())
	}

	// Save an enterprise provider with a key
	q := store.New(pool)
	encKey, _ := sec.EncryptString("sk-reused-secret-12345")
	_, err := q.PlatformSetAIProvider(t.Context(), store.PlatformSetAIProviderParams{
		TenantID:     principal.TenantID,
		Mode:         "byo",
		Provider:     "openai",
		Endpoint:     "https://api.openai.com/v1",
		Model:        "gpt-4o",
		EncryptedKey: encKey,
		Enabled:      true,
		CostCurrency: "USD",
	})
	if err != nil {
		t.Fatal(err)
	}

	// F04: Endpoint changed with empty key -> must NOT reuse stored key (refuse reuse -> 400 INVALID_PARAMS)
	req = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/ai/health", strings.NewReader(`{
		"provider": "openai",
		"endpoint": "https://changed.endpoint.test/v1",
		"model": "gpt-4o",
		"api_key": ""
	}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	authRouter.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("expected 400 when endpoint changed without key, got %d %s", w.Code, w.Body.String())
	}
}

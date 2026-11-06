package handlers

// Response-shape regression tests for the P0-4 fix: handler list/create
// responses must be explicit DTOs and must never contain store secrets
// (dkim_private_key, password_hash, sieve_script, verification_token).
// Everything here is hermetic: fake stores, no database.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

const shapeTenantID int64 = 1

func shapeTime() pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Valid: true}
}

func shapeText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: true}
}

func newShapeRouter(tenantID int64, register func(*gin.RouterGroup)) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	group := r.Group("/api/v1")
	group.Use(func(c *gin.Context) {
		c.Set(middleware.ContextTenantID, tenantID)
		c.Next()
	})
	register(group)
	return r
}

func shapeRequest(t *testing.T, r *gin.Engine, method, path, body string) (int, []byte, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(context.Background(), method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	raw := w.Body.Bytes()
	var decoded map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("%s %s returned invalid JSON: %v (%s)", method, path, err, raw)
		}
	}
	return w.Code, raw, decoded
}

func assertNoSubstrings(t *testing.T, raw []byte, forbidden ...string) {
	t.Helper()
	body := string(raw)
	for _, needle := range forbidden {
		if strings.Contains(body, needle) {
			t.Fatalf("response leaked %q: %s", needle, body)
		}
	}
}

func shapeObject(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	obj, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("expected %q to be an object, got %T (%v)", key, parent[key], parent[key])
	}
	return obj
}

func shapeArray(t *testing.T, parent map[string]any, key string) []any {
	t.Helper()
	items, ok := parent[key].([]any)
	if !ok {
		t.Fatalf("expected %q to be an array, got %T (%v)", key, parent[key], parent[key])
	}
	return items
}

func assertKeys(t *testing.T, obj map[string]any, want ...string) {
	t.Helper()
	if len(obj) != len(want) {
		t.Fatalf("unexpected fields: got %v, want exactly %v", obj, want)
	}
	for _, key := range want {
		if _, ok := obj[key]; !ok {
			t.Fatalf("missing field %q in %v", key, obj)
		}
	}
}

// --- domain fake ---

type shapeFakeDomainStore struct {
	domains      []store.ListDomainsByTenantRow
	created      store.CreateDomainRow
	listErr      error
	createErr    error
	createParams store.CreateDomainParams
	byID         store.GetDomainByIDRow
	getByIDErr   error
}

func (f *shapeFakeDomainStore) ListDomainsByTenant(_ context.Context, _ int64) ([]store.ListDomainsByTenantRow, error) {
	return f.domains, f.listErr
}

func (f *shapeFakeDomainStore) GetDomainByID(_ context.Context, _ store.GetDomainByIDParams) (store.GetDomainByIDRow, error) {
	return f.byID, f.getByIDErr
}

func (f *shapeFakeDomainStore) CreateDomain(_ context.Context, arg store.CreateDomainParams) (store.CreateDomainRow, error) {
	f.createParams = arg
	return f.created, f.createErr
}

func (f *shapeFakeDomainStore) UpdateDomainStatus(_ context.Context, arg store.UpdateDomainStatusParams) (store.UpdateDomainStatusRow, error) {
	return store.UpdateDomainStatusRow{
		ID:       arg.ID,
		TenantID: arg.TenantID,
		Status:   arg.Status,
	}, nil
}

func (f *shapeFakeDomainStore) UpdateDomainModeStatus(_ context.Context, arg store.UpdateDomainModeStatusParams) (store.UpdateDomainModeStatusRow, error) {
	return store.UpdateDomainModeStatusRow{
		ID:       arg.ID,
		TenantID: arg.TenantID,
		Mode:     arg.Mode,
		Status:   arg.Status,
	}, nil
}

func (f *shapeFakeDomainStore) DeleteDomain(_ context.Context, _ store.DeleteDomainParams) error {
	return nil
}

func (f *shapeFakeDomainStore) CountDomainsByTenant(_ context.Context, _ int64) (int64, error) {
	return 0, errors.New("no subscription")
}

func (f *shapeFakeDomainStore) GetActiveSubscriptionByTenant(_ context.Context, _ int64) (store.Subscriptions, error) {
	return store.Subscriptions{}, errors.New("no subscription")
}

func (f *shapeFakeDomainStore) GetPlanByCode(_ context.Context, _ string) (store.Plans, error) {
	return store.Plans{}, errors.New("no plan")
}

func (f *shapeFakeDomainStore) CreateOrderOnboardingTask(_ context.Context, _ store.CreateOrderOnboardingTaskParams) (store.OnboardingTasks, error) {
	return store.OnboardingTasks{}, errors.New("not implemented")
}
func (f *shapeFakeDomainStore) PlatformBindOnboardingDomain(_ context.Context, _ store.PlatformBindOnboardingDomainParams) (int64, error) {
	return 0, errors.New("not implemented")
}

func (f *shapeFakeDomainStore) RotateDomainChallenge(_ context.Context, arg store.RotateDomainChallengeParams) (store.RotateDomainChallengeRow, error) {
	return store.RotateDomainChallengeRow{ID: arg.ID, Fqdn: f.byID.Fqdn, VerificationToken: arg.VerificationToken, VerificationIssuedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}, VerificationExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(7 * 24 * time.Hour), Valid: true}}, nil
}
func (f *shapeFakeDomainStore) VerifyDomainChallenge(_ context.Context, arg store.VerifyDomainChallengeParams) (store.VerifyDomainChallengeRow, error) {
	return store.VerifyDomainChallengeRow{ID: arg.ID, TenantID: arg.TenantID, Fqdn: f.byID.Fqdn, Status: "active"}, nil
}

// --- mailbox fake ---

type shapeFakeMailboxStore struct {
	mailboxes    []store.Mailboxes
	created      store.Mailboxes
	updated      store.Mailboxes
	domain       store.GetDomainByIDRow
	listErr      error
	createErr    error
	updateErr    error
	deleteErr    error
	getDomainErr error
	createParams store.CreateMailboxParams
}

func (f *shapeFakeMailboxStore) ListMailboxesByTenant(_ context.Context, _ int64) ([]store.Mailboxes, error) {
	return f.mailboxes, f.listErr
}

func (f *shapeFakeMailboxStore) GetMailboxByID(_ context.Context, _ store.GetMailboxByIDParams) (store.Mailboxes, error) {
	return store.Mailboxes{}, errors.New("not implemented")
}

func (f *shapeFakeMailboxStore) GetDomainByID(_ context.Context, _ store.GetDomainByIDParams) (store.GetDomainByIDRow, error) {
	return f.domain, f.getDomainErr
}

func (f *shapeFakeMailboxStore) CreateMailbox(_ context.Context, arg store.CreateMailboxParams) (store.Mailboxes, error) {
	f.createParams = arg
	return f.created, f.createErr
}

func (f *shapeFakeMailboxStore) UpdateMailboxPassword(_ context.Context, _ store.UpdateMailboxPasswordParams) (store.Mailboxes, error) {
	return f.updated, f.updateErr
}

func (f *shapeFakeMailboxStore) UpdateMailboxQuota(_ context.Context, _ store.UpdateMailboxQuotaParams) (store.Mailboxes, error) {
	return f.updated, f.updateErr
}

func (f *shapeFakeMailboxStore) UpdateMailboxSettings(_ context.Context, arg store.UpdateMailboxSettingsParams) (store.UpdateMailboxSettingsRow, error) {
	return store.UpdateMailboxSettingsRow{
		ID:          arg.ID,
		TenantID:    arg.TenantID,
		DisplayName: arg.DisplayName,
		Status:      arg.Status,
		SendEnabled: arg.SendEnabled,
	}, f.updateErr
}

func (f *shapeFakeMailboxStore) DeleteMailbox(_ context.Context, _ store.DeleteMailboxParams) error {
	return f.deleteErr
}

func (f *shapeFakeMailboxStore) CountMailboxesByTenant(_ context.Context, _ int64) (int64, error) {
	return 0, errors.New("no subscription")
}

func (f *shapeFakeMailboxStore) GetActiveSubscriptionByTenant(_ context.Context, _ int64) (store.Subscriptions, error) {
	return store.Subscriptions{}, errors.New("no subscription")
}

func (f *shapeFakeMailboxStore) GetPlanByCode(_ context.Context, _ string) (store.Plans, error) {
	return store.Plans{}, errors.New("no plan")
}

func (f *shapeFakeMailboxStore) ListMailboxesWithLegalHold(_ context.Context, _ int64) ([]pgtype.Int8, error) {
	return nil, nil
}

// --- alias fake ---

type shapeFakeAliasStore struct {
	aliases      []store.ListAliasesByTenantRow
	mailboxes    []store.Mailboxes
	domain       store.GetDomainByIDRow
	created      store.MailboxAliases
	listErr      error
	createErr    error
	getDomainErr error
	createParams store.CreateAliasParams
}

func (f *shapeFakeAliasStore) ListAliasesByTenant(_ context.Context, _ int64) ([]store.ListAliasesByTenantRow, error) {
	return f.aliases, f.listErr
}

func (f *shapeFakeAliasStore) ListMailboxesByTenant(_ context.Context, _ int64) ([]store.Mailboxes, error) {
	return f.mailboxes, nil
}

func (f *shapeFakeAliasStore) GetDomainByID(_ context.Context, _ store.GetDomainByIDParams) (store.GetDomainByIDRow, error) {
	return f.domain, f.getDomainErr
}

func (f *shapeFakeAliasStore) CreateAlias(_ context.Context, arg store.CreateAliasParams) (store.MailboxAliases, error) {
	f.createParams = arg
	return f.created, f.createErr
}

func (f *shapeFakeAliasStore) DeleteAlias(_ context.Context, _ store.DeleteAliasParams) error {
	return nil
}

func (f *shapeFakeAliasStore) AliasWouldCycle(_ context.Context, _ store.AliasWouldCycleParams) (bool, error) {
	return false, nil
}

// --- tenant fake ---

type shapeFakeTenantStore struct {
	tenant       store.Tenants
	settings     []store.Settings
	sub          store.Subscriptions
	plan         store.Plans
	tenantErr    error
	settingsErr  error
	subErr       error
	planErr      error
	domainCount  int64
	mailboxCount int64
}

func (f *shapeFakeTenantStore) GetTenantByID(_ context.Context, _ int64) (store.Tenants, error) {
	return f.tenant, f.tenantErr
}

func (f *shapeFakeTenantStore) ListSettingsByScopes(_ context.Context, _ store.ListSettingsByScopesParams) ([]store.Settings, error) {
	return f.settings, f.settingsErr
}

func (f *shapeFakeTenantStore) CountDomainsByTenant(_ context.Context, _ int64) (int64, error) {
	return f.domainCount, nil
}

func (f *shapeFakeTenantStore) CountMailboxesByTenant(_ context.Context, _ int64) (int64, error) {
	return f.mailboxCount, nil
}

func (f *shapeFakeTenantStore) GetActiveSubscriptionByTenant(_ context.Context, _ int64) (store.Subscriptions, error) {
	return f.sub, f.subErr
}

func (f *shapeFakeTenantStore) GetPlanByCode(_ context.Context, _ string) (store.Plans, error) {
	return f.plan, f.planErr
}

// --- tests ---

func TestDomainListResponseShapeHasNoSecrets(t *testing.T) {
	fake := &shapeFakeDomainStore{domains: []store.ListDomainsByTenantRow{{
		ID:                7,
		TenantID:          shapeTenantID,
		Fqdn:              "example.com",
		Mode:              "managed",
		Status:            "active",
		ProvisioningState: "ready",
		VerificationToken: "verification-token-secret",
		DkimSelector:      "mail",
		DkimPrivateKey:    shapeText("DKIM-PRIVATE-KEY-MATERIAL"),
		CreatedAt:         shapeTime(),
	}}}
	h := NewDomainHandler(fake, nil)
	r := newShapeRouter(shapeTenantID, func(g *gin.RouterGroup) { g.GET("/domains", h.List) })

	code, raw, body := shapeRequest(t, r, http.MethodGet, "/api/v1/domains", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d body %s", code, raw)
	}
	assertNoSubstrings(t, raw,
		"dkim_private_key", "DKIM-PRIVATE-KEY-MATERIAL",
		"verification_token", "verification-token-secret",
		"tenant_id",
	)

	domains := shapeArray(t, body, "domains")
	if len(domains) != 1 {
		t.Fatalf("expected 1 domain, got %v", domains)
	}
	domain := domains[0].(map[string]any)
	assertKeys(t, domain, "id", "fqdn", "display_fqdn", "mode", "status", "selector", "created_at", "provisioning_state")
	if domain["fqdn"] != "example.com" || domain["mode"] != "managed" ||
		domain["status"] != "active" || domain["selector"] != "mail" || domain["provisioning_state"] != "ready" {
		t.Fatalf("unexpected domain payload: %v", domain)
	}
	if domain["id"].(float64) != 7 || domain["created_at"] == nil {
		t.Fatalf("id/created_at missing: %v", domain)
	}
}

func TestDomainCreateResponseShapeHasNoSecrets(t *testing.T) {
	fake := &shapeFakeDomainStore{created: store.CreateDomainRow{
		ID:                8,
		TenantID:          shapeTenantID,
		Fqdn:              "example.com",
		Mode:              "managed",
		Status:            "pending_dns",
		ProvisioningState: "pending",
		VerificationToken: "verification-token-secret",
		DkimSelector:      "mail",
		DkimPrivateKey:    shapeText("DKIM-PRIVATE-KEY-MATERIAL"),
		CreatedAt:         shapeTime(),
	}}
	h := NewDomainHandler(fake, nil)
	r := newShapeRouter(shapeTenantID, func(g *gin.RouterGroup) { g.POST("/domains", h.Create) })

	code, raw, body := shapeRequest(t, r, http.MethodPost, "/api/v1/domains", `{"fqdn":"Example.COM","mode":"managed"}`)
	if code != http.StatusCreated {
		t.Fatalf("status = %d body %s", code, raw)
	}
	assertNoSubstrings(t, raw,
		"dkim_private_key", "DKIM-PRIVATE-KEY-MATERIAL",
		"verification_token",
	)

	// The private key must still be persisted for signing, just not exposed.
	if !fake.createParams.DkimPrivateKey.Valid ||
		!strings.Contains(fake.createParams.DkimPrivateKey.String, "PRIVATE KEY") {
		t.Fatalf("generated DKIM private key was not passed to the store: %+v", fake.createParams.DkimPrivateKey)
	}
	if fake.createParams.Fqdn != "example.com" || fake.createParams.Mode != "managed" {
		t.Fatalf("unexpected create params: %+v", fake.createParams)
	}

	domain := shapeObject(t, body, "domain")
	assertKeys(t, domain, "id", "fqdn", "display_fqdn", "mode", "status", "selector", "created_at", "provisioning_state")
	if domain["fqdn"] != "example.com" || domain["selector"] != "mail" || domain["provisioning_state"] != "pending" {
		t.Fatalf("unexpected domain payload: %v", domain)
	}
	guidance := shapeObject(t, body, "dns_guidance")
	assertKeys(t, guidance, "mx", "spf", "dkim", "dmarc", "verification_name", "verification_value")
	if !strings.Contains(guidance["dkim"].(string), "v=DKIM1") {
		t.Fatalf("expected a public DKIM TXT record, got %v", guidance["dkim"])
	}
}

func TestMailboxListResponseShapeHasNoSecrets(t *testing.T) {
	fake := &shapeFakeMailboxStore{mailboxes: []store.Mailboxes{{
		ID:           11,
		TenantID:     shapeTenantID,
		DomainID:     3,
		LocalPart:    "alice",
		Address:      "alice@example.com",
		DisplayName:  shapeText("Alice"),
		PasswordHash: "bcrypt-password-hash-secret",
		QuotaBytes:   1024,
		Status:       "active",
		SendEnabled:  true,
		SieveScript:  shapeText("sieve-script-secret"),
		CreatedAt:    shapeTime(),
	}}}
	h := NewMailboxHandler(fake, nil)
	r := newShapeRouter(shapeTenantID, func(g *gin.RouterGroup) { g.GET("/mailboxes", h.List) })

	code, raw, body := shapeRequest(t, r, http.MethodGet, "/api/v1/mailboxes", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d body %s", code, raw)
	}
	assertNoSubstrings(t, raw,
		"password_hash", "bcrypt-password-hash-secret",
		"sieve_script", "sieve-script-secret",
		"tenant_id",
	)

	mailboxes := shapeArray(t, body, "mailboxes")
	if len(mailboxes) != 1 {
		t.Fatalf("expected 1 mailbox, got %v", mailboxes)
	}
	mailbox := mailboxes[0].(map[string]any)
	assertKeys(t, mailbox, "id", "domain_id", "local_part", "address", "display_name",
		"quota_bytes", "status", "send_enabled", "created_at")
	if mailbox["display_name"] != "Alice" {
		t.Fatalf("display_name should be a plain string, got %T %v", mailbox["display_name"], mailbox["display_name"])
	}
	if mailbox["address"] != "alice@example.com" || mailbox["quota_bytes"].(float64) != 1024 ||
		mailbox["send_enabled"] != true {
		t.Fatalf("unexpected mailbox payload: %v", mailbox)
	}
}

func TestMailboxCreateAndUpdateResponsesHideSecrets(t *testing.T) {
	fake := &shapeFakeMailboxStore{
		domain: store.GetDomainByIDRow{ID: 3, TenantID: shapeTenantID, Fqdn: "example.com"},
		created: store.Mailboxes{
			ID: 12, TenantID: shapeTenantID, DomainID: 3, LocalPart: "bob",
			Address: "bob@example.com", DisplayName: shapeText("Bob"),
			PasswordHash: "bcrypt-password-hash-secret", QuotaBytes: 2048,
			Status: "active", SendEnabled: true, SieveScript: shapeText("sieve-script-secret"),
			CreatedAt: shapeTime(),
		},
		updated: store.Mailboxes{
			ID: 12, TenantID: shapeTenantID, DomainID: 3, LocalPart: "bob",
			Address: "bob@example.com", DisplayName: shapeText("Bob"),
			PasswordHash: "updated-bcrypt-hash-secret", QuotaBytes: 4096,
			Status: "active", SendEnabled: true, SieveScript: shapeText("sieve-script-secret"),
			CreatedAt: shapeTime(),
		},
	}
	h := NewMailboxHandler(fake, nil)
	r := newShapeRouter(shapeTenantID, func(g *gin.RouterGroup) {
		g.POST("/mailboxes", h.Create)
		g.POST("/mailboxes/:id/password", h.UpdatePassword)
		g.POST("/mailboxes/:id/quota", h.UpdateQuota)
	})

	code, raw, body := shapeRequest(t, r, http.MethodPost, "/api/v1/mailboxes",
		`{"domain_id":3,"local_part":"bob","password":"secret123-password","display_name":"Bob","quota_bytes":2048}`)
	if code != http.StatusCreated {
		t.Fatalf("create status = %d body %s", code, raw)
	}
	assertNoSubstrings(t, raw, "password_hash", "bcrypt-password-hash-secret", "sieve_script", "sieve-script-secret")
	mailbox := shapeObject(t, body, "mailbox")
	assertKeys(t, mailbox, "id", "domain_id", "local_part", "address", "display_name",
		"quota_bytes", "status", "send_enabled", "created_at")
	if mailbox["address"] != "bob@example.com" || mailbox["display_name"] != "Bob" {
		t.Fatalf("unexpected mailbox payload: %v", mailbox)
	}
	if fake.createParams.PasswordHash == "" || fake.createParams.PasswordHash == "secret123-password" {
		t.Fatalf("password must be hashed before it reaches the store: %q", fake.createParams.PasswordHash)
	}

	code, raw, _ = shapeRequest(t, r, http.MethodPost, "/api/v1/mailboxes/12/password", `{"password":"newsecret-password"}`)
	if code != http.StatusOK {
		t.Fatalf("password update status = %d body %s", code, raw)
	}
	assertNoSubstrings(t, raw, "password_hash", "updated-bcrypt-hash-secret", "sieve_script", "sieve-script-secret")

	code, raw, _ = shapeRequest(t, r, http.MethodPost, "/api/v1/mailboxes/12/quota", `{"quota_bytes":4096}`)
	if code != http.StatusOK {
		t.Fatalf("quota update status = %d body %s", code, raw)
	}
	assertNoSubstrings(t, raw, "password_hash", "updated-bcrypt-hash-secret", "sieve_script", "sieve-script-secret")
}

func TestAliasListAndCreateResponseShape(t *testing.T) {
	fake := &shapeFakeAliasStore{
		aliases: []store.ListAliasesByTenantRow{{
			ID: 21, TenantID: shapeTenantID, DomainID: 3, LocalPart: "info",
			Target: "alice@example.com", Kind: "alias", Active: true,
			CreatedAt: shapeTime(), DomainFqdn: "example.com", SenderLogins: []string{"alice@example.com"},
		}},
		domain: store.GetDomainByIDRow{ID: 3, TenantID: shapeTenantID, Fqdn: "example.com"},
		created: store.MailboxAliases{
			ID: 22, TenantID: shapeTenantID, DomainID: 3, LocalPart: "sales",
			Target: "bob@example.com", Kind: "alias", Active: true, CreatedAt: shapeTime(),
		},
	}
	h := NewAliasHandler(fake, nil)
	r := newShapeRouter(shapeTenantID, func(g *gin.RouterGroup) {
		g.GET("/aliases", h.List)
		g.POST("/aliases", h.Create)
	})

	code, raw, body := shapeRequest(t, r, http.MethodGet, "/api/v1/aliases", "")
	if code != http.StatusOK {
		t.Fatalf("list status = %d body %s", code, raw)
	}
	assertNoSubstrings(t, raw, "tenant_id")
	aliases := shapeArray(t, body, "aliases")
	alias := aliases[0].(map[string]any)
	assertKeys(t, alias, "id", "domain_id", "domain_fqdn", "local_part", "target", "kind", "active", "created_at", "sender_logins")
	grants, ok := alias["sender_logins"].([]any)
	if !ok || len(grants) != 1 || grants[0] != "alice@example.com" {
		t.Fatal("explicit sender grants hidden from alias response")
	}
	if alias["domain_fqdn"] != "example.com" || alias["local_part"] != "info" {
		t.Fatalf("unexpected alias payload: %v", alias)
	}

	code, raw, body = shapeRequest(t, r, http.MethodPost, "/api/v1/aliases",
		`{"domain_id":3,"local_part":"sales","target":"bob@example.com","kind":"alias"}`)
	if code != http.StatusCreated {
		t.Fatalf("create status = %d body %s", code, raw)
	}
	assertNoSubstrings(t, raw, "tenant_id")
	created := shapeObject(t, body, "alias")
	assertKeys(t, created, "id", "domain_id", "domain_fqdn", "local_part", "target", "kind", "active", "created_at", "sender_logins")
	if created["domain_fqdn"] != "example.com" || created["target"] != "bob@example.com" {
		t.Fatalf("unexpected created alias: %v", created)
	}
}

func TestTenantCurrentResponseShape(t *testing.T) {
	fake := &shapeFakeTenantStore{
		tenant: store.Tenants{
			ID: 1, BrandID: 2, Name: "Acme", Slug: "acme", Status: "active",
			OwnerUserID: pgtype.Int8{Int64: 99, Valid: true},
			Settings:    []byte(`{"tenant-secret":"do-not-leak"}`),
			CreatedAt:   shapeTime(),
		},
		settings: []store.Settings{
			{Scope: "tenant", ScopeID: 1, Key: "locale", Value: []byte(`"zh-CN"`)},
			{Scope: "tenant", ScopeID: 1, Key: "mailbox_domain_limit", Value: []byte(`5`)},
			{Scope: "tenant", ScopeID: 1, Key: "mailbox_limit", Value: []byte(`50`)},
			{Scope: "tenant", ScopeID: 1, Key: "mailbox_quota_bytes", Value: []byte(`1073741824`)},
		},
		sub:          store.Subscriptions{PlanID: "pro", Status: "active"},
		plan:         store.Plans{Code: "pro", Name: []byte(`{"zh-CN":"专业版","en":"Pro"}`), DomainLimit: 5, MailboxLimit: 50, QuotaBytes: 1 << 30},
		domainCount:  2,
		mailboxCount: 3,
	}
	h := NewTenantHandler(fake, nil)
	r := newShapeRouter(shapeTenantID, func(g *gin.RouterGroup) { g.GET("/tenants/current", h.Current) })

	code, raw, body := shapeRequest(t, r, http.MethodGet, "/api/v1/tenants/current", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d body %s", code, raw)
	}
	assertNoSubstrings(t, raw, "tenant-secret", "brand_id", "owner_user_id")

	tenant := shapeObject(t, body, "tenant")
	assertKeys(t, tenant, "id", "name", "slug", "status", "created_at")
	if tenant["name"] != "Acme" || tenant["slug"] != "acme" {
		t.Fatalf("unexpected tenant payload: %v", tenant)
	}
	settings := shapeObject(t, body, "settings")
	if settings["locale"] != "zh-CN" {
		t.Fatalf("settings did not survive: %v", settings)
	}
	usage := shapeObject(t, body, "usage")
	assertKeys(t, usage, "domains_used", "domains_limit", "mailboxes_used", "mailboxes_limit", "storage_bytes_limit", "storage_bytes_used", "storage_bytes_allocated", "storage_measured_at", "storage_error", "storage_status")
	if usage["domains_used"].(float64) != 2 || usage["mailboxes_used"].(float64) != 3 ||
		usage["domains_limit"].(float64) != 5 || usage["mailboxes_limit"].(float64) != 50 {
		t.Fatalf("unexpected usage payload: %v", usage)
	}
	subscription := shapeObject(t, body, "subscription")
	assertKeys(t, subscription, "plan_code", "plan_name", "status")
	planName := shapeObject(t, subscription, "plan_name")
	if planName["zh-CN"] != "专业版" || subscription["plan_code"] != "pro" {
		t.Fatalf("unexpected subscription payload: %v", subscription)
	}
}

func TestHandlerErrorPathsRemainIntact(t *testing.T) {
	domainFake := &shapeFakeDomainStore{listErr: errors.New("db down")}
	dh := NewDomainHandler(domainFake, nil)
	dr := newShapeRouter(shapeTenantID, func(g *gin.RouterGroup) { g.GET("/domains", dh.List) })
	code, _, body := shapeRequest(t, dr, http.MethodGet, "/api/v1/domains", "")
	if code != http.StatusInternalServerError {
		t.Fatalf("domain list error status = %d, want 500", code)
	}
	if shapeObject(t, body, "error")["code"] != "DB_ERROR" {
		t.Fatalf("unexpected error body: %v", body)
	}

	mailboxFake := &shapeFakeMailboxStore{getDomainErr: errors.New("not found")}
	mh := NewMailboxHandler(mailboxFake, nil)
	mr := newShapeRouter(shapeTenantID, func(g *gin.RouterGroup) {
		g.POST("/mailboxes", mh.Create)
		g.DELETE("/mailboxes/:id", mh.Delete)
	})
	code, _, body = shapeRequest(t, mr, http.MethodPost, "/api/v1/mailboxes",
		`{"domain_id":999,"local_part":"x","password":"secret123-password"}`)
	if code != http.StatusNotFound {
		t.Fatalf("mailbox create missing domain status = %d, want 404", code)
	}
	if shapeObject(t, body, "error")["code"] != "DOMAIN_NOT_FOUND" {
		t.Fatalf("unexpected error body: %v", body)
	}
	code, _, _ = shapeRequest(t, mr, http.MethodDelete, "/api/v1/mailboxes/not-a-number", "")
	if code != http.StatusBadRequest {
		t.Fatalf("delete invalid id status = %d, want 400", code)
	}

	aliasFake := &shapeFakeAliasStore{
		domain:    store.GetDomainByIDRow{ID: 3, TenantID: shapeTenantID, Fqdn: "example.com"},
		createErr: errors.New("duplicate"),
	}
	ah := NewAliasHandler(aliasFake, nil)
	ar := newShapeRouter(shapeTenantID, func(g *gin.RouterGroup) { g.POST("/aliases", ah.Create) })
	code, _, body = shapeRequest(t, ar, http.MethodPost, "/api/v1/aliases",
		`{"domain_id":3,"local_part":"sales","target":"bob@example.com","kind":"alias"}`)
	if code != http.StatusConflict {
		t.Fatalf("alias conflict status = %d, want 409", code)
	}
	if shapeObject(t, body, "error")["code"] != "ALIAS_CONFLICT" {
		t.Fatalf("unexpected error body: %v", body)
	}
}

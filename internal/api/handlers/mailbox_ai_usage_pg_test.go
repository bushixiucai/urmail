package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/airetrieval"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/aianswer"
	"github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// fixturePrincipalSessions delegates to the offline fake but rewrites every
// session identity to the real fixture tenant and mailbox.
type fixturePrincipalSessions struct {
	inner   *fakeMailboxSessions
	tenant  int64
	mailbox int64
}

func (f *fixturePrincipalSessions) rewrite(session webmail.Session) webmail.Session {
	session.TenantID, session.MailboxID = f.tenant, f.mailbox
	return session
}

func (f *fixturePrincipalSessions) Create(ctx context.Context, tokenHash, address, ip string) (webmail.Session, error) {
	session, err := f.inner.Create(ctx, tokenHash, address, ip)
	return f.rewrite(session), err
}

func (f *fixturePrincipalSessions) GetMailboxSession(ctx context.Context, tokenHash string) (webmail.Session, error) {
	session, err := f.inner.GetMailboxSession(ctx, tokenHash)
	return f.rewrite(session), err
}

func (f *fixturePrincipalSessions) Get(ctx context.Context, tokenHash string) (webmail.Session, error) {
	session, err := f.inner.Get(ctx, tokenHash)
	return f.rewrite(session), err
}

func (f *fixturePrincipalSessions) Revoke(ctx context.Context, tokenHash string) error {
	return f.inner.Revoke(ctx, tokenHash)
}

// newPGAskRouter mounts the search-ask route authenticated as the fixture
// mailbox principal (real fixture tenant and mailbox IDs, fake transport).
func newPGAskRouter(t *testing.T, principal store.PlatformConsumeMailboxTicketRow, build func(webmail.CredentialVault) *AISearchAsk) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.ReleaseMode)
	loginAttempts = newLoginLimiter()
	password := "correct-horse-battery"
	hash, err := mail.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	// The shared offline fake hardcodes tenant 7 / mailbox 9; the accounting
	// rows must belong to the real fixture principal instead.
	sessions := &fixturePrincipalSessions{inner: &fakeMailboxSessions{}, tenant: principal.TenantID, mailbox: principal.MailboxID}
	vault := webmail.NewMemoryVault()
	native := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{tenantID: principal.TenantID, mailboxID: principal.MailboxID, address: principal.Address, hash: hash}, Sessions: sessions, Vault: vault}
	r := newNativeTestRouter(t, native, sessions)
	auth := r.Group("/api/v1/mailbox")
	auth.Use(middleware.RequireMailboxAuth(sessions))
	auth.POST("/ai/search-ask", build(vault).Handle)
	t.Cleanup(func() { loginAttempts = newLoginLimiter() })
	return r
}

// askPG asks one grounded question against the migrated test database using
// the fixture mailbox principal for accounting.
func askPG(t *testing.T, pool *db.Pool, principal store.PlatformConsumeMailboxTicketRow) *httptest.ResponseRecorder {
	t.Helper()
	source := &airetrieval.FakeSource{Mail: map[string]map[uint32]string{"INBOX": {5: "invoice total 42"}}, Validity: map[string]uint32{"INBOX": 4}}
	storeFake := airetrieval.NewFakeStore()
	airPrincipal := airetrieval.Principal{TenantID: principal.TenantID, MailboxID: principal.MailboxID}
	if _, err := (airetrieval.Indexer{Store: storeFake, Source: source}).Reindex(context.Background(), airPrincipal); err != nil {
		t.Fatal(err)
	}
	model := func(context.Context, string, []aianswer.Source) (string, []string, error) {
		return "42 euros.", []string{"1"}, nil
	}
	handler := &AISearchAsk{Pool: pool, Store: storeFake, Dialer: fakeAskDialer{client: &fakeAskIMAP{messages: map[string]map[uint32]string{"INBOX": {5: "invoice total 42"}}, validity: map[string]uint32{"INBOX": 4}}}, Vault: webmail.NewMemoryVault(), ModelFor: func(context.Context, int64) (aianswer.Model, error) { return model, nil }}
	handler.Accounting = AISearchAskAccounting{Pool: pool}
	r := newPGAskRouter(t, principal, func(vault webmail.CredentialVault) *AISearchAsk { handler.Vault = vault; return handler })
	native := loginNative(t, r, principal.Address, "correct-horse-battery")
	return askOnce(t, r, native, "invoice")
}

func TestAISearchAskWritesReservationRowPerAskPG(t *testing.T) {
	p, principal := mailboxAIFixture(t, 10)
	rec := askPG(t, p, principal)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rows, err := store.New(p).PlatformAIUsage(context.Background(), principal.TenantID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("reservation rows=%d err=%v", len(rows), err)
	}
	if !strings.HasPrefix(rows[0].RequestKey, "ask-") || rows[0].Status != "reserved" {
		t.Fatalf("unexpected reservation row %+v", rows[0])
	}
}

func TestAISearchAskBudgetEnforcedAcrossTwoRequestsPG(t *testing.T) {
	p, principal := mailboxAIFixture(t, 10)
	// A daily token budget that only the first worst-case reservation fits:
	// len(question)+4096 tokens are reserved per ask.
	if _, err := p.Exec(context.Background(), "UPDATE ai_provider_configs SET daily_token_limit=$1 WHERE tenant_id=$2", 4200, principal.TenantID); err != nil {
		t.Fatal(err)
	}
	first := askPG(t, p, principal)
	if first.Code != http.StatusOK {
		t.Fatalf("first ask under budget failed: %d %s", first.Code, first.Body.String())
	}
	second := askPG(t, p, principal)
	if second.Code != http.StatusTooManyRequests || !strings.Contains(second.Body.String(), "AI_BUDGET_EXCEEDED") {
		t.Fatalf("second ask crossed the budget without AI_BUDGET_EXCEEDED: %d %s", second.Code, second.Body.String())
	}
	rows, err := store.New(p).PlatformAIUsage(context.Background(), principal.TenantID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("over-budget ask must not write a reservation: rows=%d err=%v", len(rows), err)
	}
}

func TestAISearchAskAnswerSurvivesAccountingPG(t *testing.T) {
	p, principal := mailboxAIFixture(t, 10)
	rec := askPG(t, p, principal)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "\"grounded\":true") {
		t.Fatalf("answer failed: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAISearchAskAccountingReserveAndSettlePG(t *testing.T) {
	p, principal := mailboxAIFixture(t, 10)
	accounting := AISearchAskAccounting{Pool: p}
	reservation, err := accounting.Reserve(context.Background(), principal.TenantID, principal.MailboxID, "invoice")
	if err != nil || reservation == nil {
		t.Fatalf("reserve: %v %v", reservation, err)
	}
	// Settle is asynchronous and best-effort by contract: a provider failure
	// settles to uncertain without failing anything.
	reservation.Settle(errors.New("provider down"), false, 0, 0, func(_, _ int64) (int64, error) { return 0, nil })
	time.Sleep(300 * time.Millisecond)
	rows, err := store.New(p).PlatformAIUsage(context.Background(), principal.TenantID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
}

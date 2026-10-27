package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/handlers"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/reconcile"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// seedSpamFixture creates tenant A with mailboxes one/two and tenant B with
// mailbox one, and returns the ids.
func seedSpamFixture(t *testing.T, platform *db.Pool) (tenantA, boxOneA, boxTwoA, tenantB, boxOneB int64) {
	t.Helper()
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, domainA, domainB int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('spamfb','SpamFb') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'A','spamfb-a','active') RETURNING id", brand).Scan(&tenantA); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'B','spamfb-b','active') RETURNING id", brand).Scan(&tenantB); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'a-fb.test','active','tok',now()) RETURNING id", tenantA).Scan(&domainA); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'b-fb.test','active','tok',now()) RETURNING id", tenantB).Scan(&domainB); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'one','one@a-fb.test','x','active') RETURNING id", tenantA, domainA).Scan(&boxOneA); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'two','two@a-fb.test','x','active') RETURNING id", tenantA, domainA).Scan(&boxTwoA); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'one','one@b-fb.test','x','active') RETURNING id", tenantB, domainB).Scan(&boxOneB); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return tenantA, boxOneA, boxTwoA, tenantB, boxOneB
}

// spamFeedbackServer builds a gin engine whose /feedback route authenticates
// as the given mailbox principal (the same session the bearer middleware
// installs in production).
func spamFeedbackServer(pool *db.Pool, session webmail.Session) http.Handler {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.ContextMailboxSession, session)
		c.Next()
	})
	r.POST("/feedback", (&handlers.MailboxSpamFeedback{Pool: pool}).Feedback)
	return r
}

func postFeedback(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/feedback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

const spamFeedbackBody = `{"message_id":"<spam-1@bulk.example>","folder":"INBOX","verdict":"spam","from_address":"Sales@Bulk.Example","subject":"cheap watches now"}`

func TestMailboxSpamFeedbackWritesRulesAndCounts(t *testing.T) {
	platform, app := testdb.OpenMigrated(t)
	tenantA, boxOneA, _, _, _ := seedSpamFixture(t, platform)
	ctx := context.Background()
	handler := spamFeedbackServer(app, webmail.Session{TenantID: tenantA, MailboxID: boxOneA})

	if w := postFeedback(t, handler, spamFeedbackBody); w.Code != http.StatusOK {
		t.Fatalf("feedback rejected: %d %s", w.Code, w.Body.String())
	}

	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	var feedback int64
	if feedback, err = q.MailboxSpamCountFeedback(ctx, store.MailboxSpamCountFeedbackParams{TenantID: tenantA, MailboxID: boxOneA}); err != nil {
		t.Fatal(err)
	}
	if feedback != 1 {
		t.Fatalf("expected 1 feedback row, got %d", feedback)
	}
	rules, err := q.MailboxSpamListSenderRules(ctx, store.MailboxSpamListSenderRulesParams{
		TenantID: tenantA, MailboxID: boxOneA, SenderAddress: "sales@bulk.example", SenderDomain: "bulk.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].List != "deny" || rules[0].Scope != "mailbox" {
		t.Fatalf("deny rule not written: %+v", rules)
	}
	var tokens int64
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM mailbox_spam_tokens WHERE tenant_id=$1 AND mailbox_id=$2 AND token='dom:bulk'", tenantA, boxOneA).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if tokens != 1 {
		t.Fatalf("sender token not learned: %d", tokens)
	}

	// Idempotency: re-reporting the same message with the same verdict must
	// not create a second row.
	handler2 := spamFeedbackServer(app, webmail.Session{TenantID: tenantA, MailboxID: boxOneA})
	if w := postFeedback(t, handler2, spamFeedbackBody); w.Code != http.StatusOK {
		t.Fatalf("replay rejected: %d", w.Code)
	}
	if feedback, err = q.MailboxSpamCountFeedback(ctx, store.MailboxSpamCountFeedbackParams{TenantID: tenantA, MailboxID: boxOneA}); err != nil {
		t.Fatal(err)
	}
	if feedback != 1 {
		t.Fatalf("replay duplicated feedback: %d", feedback)
	}

	// The latest verdict wins: reporting ham later flips deny to allow.
	hamBody := `{"message_id":"<spam-1@bulk.example>","folder":"INBOX","verdict":"ham","from_address":"sales@bulk.example"}`
	if w := postFeedback(t, handler2, hamBody); w.Code != http.StatusOK {
		t.Fatalf("ham feedback rejected: %d", w.Code)
	}
	rules, err = q.MailboxSpamListSenderRules(ctx, store.MailboxSpamListSenderRulesParams{
		TenantID: tenantA, MailboxID: boxOneA, SenderAddress: "sales@bulk.example", SenderDomain: "bulk.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].List != "allow" {
		t.Fatalf("verdict flip did not replace the rule: %+v", rules)
	}
}

func TestMailboxSpamFeedbackRLSIsolation(t *testing.T) {
	platform, app := testdb.OpenMigrated(t)
	tenantA, boxOneA, boxTwoA, tenantB, boxOneB := seedSpamFixture(t, platform)
	ctx := context.Background()
	reporter := spamFeedbackServer(app, webmail.Session{TenantID: tenantA, MailboxID: boxOneA})
	if w := postFeedback(t, reporter, spamFeedbackBody); w.Code != http.StatusOK {
		t.Fatalf("feedback rejected: %d", w.Code)
	}

	// Mailbox two (same tenant) sees neither the feedback row nor the rule.
	sibling, err := app.MailboxTx(ctx, tenantA, boxTwoA)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sibling.Rollback(ctx) }()
	siblingQueries := store.New(sibling)
	count, err := siblingQueries.MailboxSpamCountFeedback(ctx, store.MailboxSpamCountFeedbackParams{TenantID: tenantA, MailboxID: boxTwoA})
	if err != nil || count != 0 {
		t.Fatalf("sibling mailbox saw feedback rows: %d %v", count, err)
	}
	rules, err := siblingQueries.MailboxSpamListSenderRules(ctx, store.MailboxSpamListSenderRulesParams{
		TenantID: tenantA, MailboxID: boxTwoA, SenderAddress: "sales@bulk.example", SenderDomain: "bulk.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 0 {
		t.Fatalf("sibling mailbox sees mailbox one's rules: %+v", rules)
	}
	// Writing mailbox one's rule while scoped to mailbox two must violate
	// the WITH CHECK clause even with a forged predicate. The violation
	// aborts that transaction, so it runs in its own one.
	if err := func() error {
		violator, err := app.MailboxTx(ctx, tenantA, boxTwoA)
		if err != nil {
			return err
		}
		defer func() { _ = violator.Rollback(ctx) }()
		_, err = store.New(violator).MailboxSpamSetSenderList(ctx, store.MailboxSpamSetSenderListParams{
			TenantID: tenantA, MailboxID: boxOneA, Address: "hijack@bulk.example", Domain: "bulk.example", List: "allow",
		})
		return err
	}(); err == nil {
		t.Fatal("RLS did not stop a cross-mailbox rule write")
	}
	_ = sibling.Rollback(ctx)

	// Tenant B cannot touch tenant A at all.
	foreign, err := app.MailboxTx(ctx, tenantB, boxOneB)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreign.Rollback(ctx) }()
	foreignQueries := store.New(foreign)
	count, countErr := foreignQueries.MailboxSpamCountFeedback(ctx, store.MailboxSpamCountFeedbackParams{TenantID: tenantB, MailboxID: boxOneB})
	if countErr != nil || count != 0 {
		t.Fatalf("foreign tenant saw feedback rows: %d %v", count, countErr)
	}
	// The forged cross-tenant write violates WITH CHECK in its own tx.
	if err := func() error {
		violator, err := app.MailboxTx(ctx, tenantB, boxOneB)
		if err != nil {
			return err
		}
		defer func() { _ = violator.Rollback(ctx) }()
		_, err = store.New(violator).MailboxSpamSetSenderList(ctx, store.MailboxSpamSetSenderListParams{
			TenantID: tenantA, MailboxID: boxOneA, Address: "foreign@bulk.example", Domain: "bulk.example", List: "deny",
		})
		return err
	}(); err == nil {
		t.Fatal("RLS did not stop a cross-tenant rule write")
	}
	foreignRules, err := foreignQueries.MailboxSpamListSenderRules(ctx, store.MailboxSpamListSenderRulesParams{
		TenantID: tenantA, MailboxID: boxOneA, SenderAddress: "sales@bulk.example", SenderDomain: "bulk.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(foreignRules) != 0 {
		t.Fatalf("foreign tenant can read tenant A rules: %+v", foreignRules)
	}
}

func TestMailboxSpamFeedbackValidation(t *testing.T) {
	platform, app := testdb.OpenMigrated(t)
	tenantA, boxOneA, _, _, _ := seedSpamFixture(t, platform)
	handler := spamFeedbackServer(app, webmail.Session{TenantID: tenantA, MailboxID: boxOneA})
	cases := []struct {
		name string
		body string
		code int
	}{
		{"bad verdict", `{"message_id":"<m@x>","folder":"INBOX","verdict":"maybe"}`, http.StatusBadRequest},
		{"empty message id", `{"message_id":"","folder":"INBOX","verdict":"spam"}`, http.StatusBadRequest},
		{"newline in message id", "{\"message_id\":\"a\\nb\",\"folder\":\"INBOX\",\"verdict\":\"spam\"}", http.StatusBadRequest},
		{"empty folder", `{"message_id":"<m@x>","folder":"","verdict":"spam"}`, http.StatusBadRequest},
		{"oversize subject", `{"message_id":"<m@x>","folder":"INBOX","verdict":"spam","subject":"` + strings.Repeat("x", 1000) + `"}`, http.StatusBadRequest},
		{"invalid sender is recorded without rule", `{"message_id":"<m2@x>","folder":"INBOX","verdict":"spam","from_address":"not an address"}`, http.StatusOK},
	}
	for _, tc := range cases {
		if w := postFeedback(t, handler, tc.body); w.Code != tc.code {
			t.Fatalf("%s: got %d want %d (%s)", tc.name, w.Code, tc.code, w.Body.String())
		}
	}
	// The invalid-sender feedback row exists, but no rule was poisoned.
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rules, err := store.New(tx).MailboxSpamListSenderRules(ctx, store.MailboxSpamListSenderRulesParams{
		TenantID: tenantA, MailboxID: boxOneA, SenderAddress: "", SenderDomain: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 0 {
		t.Fatalf("invalid sender created a rule: %+v", rules)
	}
}

func TestMailboxSpamFeedbackRequiresPoolAndSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	noPool := gin.New()
	noPool.POST("/feedback", (&handlers.MailboxSpamFeedback{Pool: nil}).Feedback)
	if w := postFeedback(t, noPool, spamFeedbackBody); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil pool: got %d", w.Code)
	}
	noSession := gin.New()
	noSession.POST("/feedback", (&handlers.MailboxSpamFeedback{Pool: nil}).Feedback)
	if w := postFeedback(t, noSession, spamFeedbackBody); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing session: got %d", w.Code)
	}
}

type spamReconcileObserver struct{ calls int }

func (r *spamReconcileObserver) Trigger() { r.calls++ }

func TestSpamFeedbackTriggersScopedConfigurationSnapshot(t *testing.T) {
	platform, app := testdb.OpenMigrated(t)
	tenantA, boxA, _, _, _ := seedSpamFixture(t, platform)
	observer := &spamReconcileObserver{}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.ContextMailboxSession, webmail.Session{TenantID: tenantA, MailboxID: boxA})
		c.Next()
	})
	router.POST("/feedback", (&handlers.MailboxSpamFeedback{Pool: app, Reconcile: observer}).Feedback)
	w := postFeedback(t, router, spamFeedbackBody)
	if w.Code != http.StatusOK || observer.calls != 1 {
		t.Fatalf("feedback/reconcile: %d calls=%d %s", w.Code, observer.calls, w.Body.String())
	}
	cfg := &config.Config{MyHostname: "box.a-fb.test", PrimaryDomain: "a-fb.test", StorageRoot: "/var/mail", DatabaseURL: "host=localhost user=urmail_dovecot", ListenAddr: "127.0.0.1:8080"}
	snapshot, err := reconcile.AssembleConfigContext(t.Context(), cfg, store.New(platform.Pool), "192.0.2.1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.DovecotNode.SpamPolicies) != 1 {
		t.Fatalf("snapshot policies=%v", snapshot.DovecotNode.SpamPolicies)
	}
	policy := snapshot.DovecotNode.SpamPolicies[0]
	if policy.MailboxAddress != "one@a-fb.test" || policy.SenderAddress != "sales@bulk.example" || policy.List != "deny" {
		t.Fatalf("wrong delivery scope: %+v", policy)
	}
	var before, after int64
	if err = platform.Pool.QueryRow(t.Context(), "SELECT sum(spam_count) FROM mailbox_spam_tokens WHERE tenant_id=$1 AND mailbox_id=$2", tenantA, boxA).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if w = postFeedback(t, router, spamFeedbackBody); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if err = platform.Pool.QueryRow(t.Context(), "SELECT sum(spam_count) FROM mailbox_spam_tokens WHERE tenant_id=$1 AND mailbox_id=$2", tenantA, boxA).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("duplicate feedback trained twice: %d -> %d", before, after)
	}
}

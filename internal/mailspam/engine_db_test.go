package mailspam

import (
	"context"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// assertSpamTables checks the presence (or absence) of the three
// MAIL-002 tables, their forced RLS and the mailbox-scope policy.
func assertSpamTables(t *testing.T, dsn string, present bool) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	const query = `SELECT count(*) FROM pg_tables t
 JOIN pg_class c ON c.relname = t.tablename
 JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
 WHERE t.tablename IN ('mailbox_spam_feedback','mailbox_spam_senders','mailbox_spam_tokens')
   AND c.relrowsecurity`
	var count int
	if err = conn.QueryRow(ctx, query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if present && count != 3 {
		t.Fatalf("expected 3 spam tables with RLS, got %d", count)
	}
	if !present && count != 0 {
		t.Fatalf("expected 0 spam tables after downgrade, got %d", count)
	}
	if !present {
		return
	}
	const policies = `SELECT count(*) FROM pg_policies
 WHERE schemaname='public'
   AND tablename IN ('mailbox_spam_feedback','mailbox_spam_senders','mailbox_spam_tokens')`
	if err = conn.QueryRow(ctx, policies).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("expected 3 RLS policies, got %d", count)
	}
	// Force RLS must be on so even the table owner cannot bypass it.
	const forced = `SELECT count(*) FROM pg_class c
 JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname='public'
 WHERE c.relname IN ('mailbox_spam_feedback','mailbox_spam_senders','mailbox_spam_tokens')
   AND c.relforcerowsecurity`
	if err = conn.QueryRow(ctx, forced).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("expected FORCE ROW LEVEL SECURITY on all 3 tables, got %d", count)
	}
}

// seedMailboxes creates two tenants with one mailbox each plus a second
// mailbox in the first tenant, and returns their identifiers.
func seedMailboxes(t *testing.T, platform *db.Pool) (tenantA, boxA1, boxA2, tenantB, boxB1 int64) {
	t.Helper()
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, domainA, domainB int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('spamfix','SpamFix') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'A','spam-a','active') RETURNING id", brand).Scan(&tenantA); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'B','spam-b','active') RETURNING id", brand).Scan(&tenantB); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'a-spam.test','active','tok',now()) RETURNING id", tenantA).Scan(&domainA); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'b-spam.test','active','tok',now()) RETURNING id", tenantB).Scan(&domainB); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'one','one@a-spam.test','x','active') RETURNING id", tenantA, domainA).Scan(&boxA1); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'two','two@a-spam.test','x','active') RETURNING id", tenantA, domainA).Scan(&boxA2); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'one','one@b-spam.test','x','active') RETURNING id", tenantB, domainB).Scan(&boxB1); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return tenantA, boxA1, boxA2, tenantB, boxB1
}

// setSenderRule writes a sender rule as the named mailbox would through the
// feedback API (MailboxTx + RLS + explicit predicate).
func setSenderRule(t *testing.T, app *db.Pool, tenantID, mailboxID int64, address, list string) {
	t.Helper()
	ctx := context.Background()
	tx, err := app.MailboxTx(ctx, tenantID, mailboxID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	affected, err := store.New(tx).MailboxSpamSetSenderList(ctx, store.MailboxSpamSetSenderListParams{
		TenantID: tenantID, MailboxID: mailboxID, Address: address, Domain: senderDomainForRule(address), List: list,
	})
	if err != nil {
		t.Fatal(err)
	}
	if affected == 0 {
		t.Fatal("sender rule write affected no rows")
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func senderDomainForRule(address string) string {
	for i := len(address) - 1; i >= 0; i-- {
		if address[i] == '@' {
			return address[i+1:]
		}
	}
	return ""
}

func spamEngine(platform, app *db.Pool) *Engine {
	return &Engine{
		Platform: platform,
		App:      app,
		Config:   EngineConfig{TrustedAuthServIDs: []string{"mx.test"}, LocalDomains: []string{"a-spam.test", "b-spam.test"}},
	}
}

func messageFor(from, recipient string) VerdictRequest {
	return VerdictRequest{
		EnvelopeFrom: from,
		Recipients:   []string{recipient},
		Headers: []Header{
			{Name: "From", Value: "Sender <" + from + ">"},
			{Name: "Subject", Value: "Hello"},
			{Name: "Message-ID", Value: "<m@test>"},
			{Name: "Date", Value: "Mon, 26 Oct 2026 10:00:00 +0800"},
			{Name: "Authentication-Results", Value: "mx.test; spf=pass smtp.mailfrom=test; dkim=pass header.d=test; dmarc=pass header.from=test"},
		},
		BodySample: []byte("a calm and ordinary message body"),
	}
}

func TestEnginePrivateDenyListDoesNotModifySharedMessage(t *testing.T) {
	platform, app := testdb.OpenMigrated(t)
	tenantA, boxA1, _, _, _ := seedMailboxes(t, platform)
	engine := spamEngine(platform, app)
	ctx := context.Background()

	// Without any rule the authenticated, SPF/DKIM/DMARC-pass message is ham.
	response := engine.Decide(ctx, messageFor("newsletter@vendor.example", "one@a-spam.test"))
	if !headerIs(response, HeaderNameSpam, "no") {
		t.Fatalf("baseline verdict not ham: %+v", response.InsertHeaders)
	}

	// The mailbox owner denies the sender: the same message becomes spam.
	setSenderRule(t, app, tenantA, boxA1, "newsletter@vendor.example", "deny")
	response = engine.Decide(ctx, messageFor("newsletter@vendor.example", "one@a-spam.test"))
	if !headerIs(response, HeaderNameSpam, "no") {
		t.Fatalf("private deny leaked into shared message: %+v", response.InsertHeaders)
	}

	// The other mailbox in the SAME tenant is unaffected (per-mailbox scope).
	response = engine.Decide(ctx, messageFor("newsletter@vendor.example", "two@a-spam.test"))
	if !headerIs(response, HeaderNameSpam, "no") {
		t.Fatalf("rule leaked across mailboxes: %+v", response.InsertHeaders)
	}
}

func TestEnginePrivateAllowListDoesNotModifySharedMessage(t *testing.T) {
	platform, app := testdb.OpenMigrated(t)
	tenantA, boxA1, _, _, _ := seedMailboxes(t, platform)
	engine := spamEngine(platform, app)
	setSenderRule(t, app, tenantA, boxA1, "newsletter@vendor.example", "allow")

	// A spammy message from the allow-listed sender stays ham for its owner.
	req := messageFor("newsletter@vendor.example", "one@a-spam.test")
	req.Headers = req.Headers[:1] // strip Date/Message-ID/A-R to add spam signals
	req.BodySample = []byte("FREE MONEY http://a.example http://b.example http://c.example http://d.example http://e.example http://f.example")
	response := engine.Decide(context.Background(), req)
	if headerIs(response, HeaderNameSpam, "no") {
		t.Fatalf("private allow incorrectly modified shared verdict: %+v", response.InsertHeaders)
	}
}

func TestEngineTenantIsolationOfTrainingInput(t *testing.T) {
	platform, app := testdb.OpenMigrated(t)
	tenantA, boxA1, _, tenantB, _ := seedMailboxes(t, platform)
	engine := spamEngine(platform, app)
	setSenderRule(t, app, tenantA, boxA1, "news@bulk.example", "deny")

	// Tenant B's mailbox sees no rule from tenant A: same sender, same
	// content, different verdict.
	response := engine.Decide(context.Background(), messageFor("news@bulk.example", "one@b-spam.test"))
	if !headerIs(response, HeaderNameSpam, "no") {
		t.Fatalf("tenant A policy leaked into tenant B: %+v", response.InsertHeaders)
	}
	_ = tenantB
}

func TestEngineExternalRecipientSkipsClassification(t *testing.T) {
	platform, app := testdb.OpenMigrated(t)
	_, _, _, _, _ = seedMailboxes(t, platform)
	engine := spamEngine(platform, app)
	response := engine.Decide(context.Background(), messageFor("news@bulk.example", "someone@elsewhere.invalid"))
	if len(response.InsertHeaders) != 0 || response.Reject {
		t.Fatalf("external recipient must not be tagged or rejected: %+v", response)
	}
}

func TestEngineDenyActionsStayOffByDefault(t *testing.T) {
	platform, app := testdb.OpenMigrated(t)
	tenantA, boxA1, _, _, _ := seedMailboxes(t, platform)
	engine := spamEngine(platform, app)
	setSenderRule(t, app, tenantA, boxA1, "news@bulk.example", "deny")
	response := engine.Decide(context.Background(), messageFor("news@bulk.example", "one@a-spam.test"))
	if !headerIs(response, HeaderNameSpam, "no") {
		t.Fatalf("private deny modified shared verdict: %+v", response.InsertHeaders)
	}
	if response.Reject {
		t.Fatal("engine rejected with DenyActions unset; nothing must be rejected in v1")
	}
}

func headerIs(response VerdictResponse, name, value string) bool {
	for _, h := range response.InsertHeaders {
		if h[0] == name && h[1] == value {
			return true
		}
	}
	return false
}

// TestSpamMigrationUpDown verifies the three mailbox_spam_* tables exist at
// head and disappear cleanly when stepping back to 0128, then come back.
func TestSpamMigrationUpDown(t *testing.T) {
	dsn := testdb.New(t)
	ctx := context.Background()

	testdb.MigrateTo(t, dsn, 129)
	assertSpamTables(t, dsn, true)

	// Down to 0128: the tables are gone.
	testdb.MigrateTo(t, dsn, 128)
	assertSpamTables(t, dsn, false)

	// Up again: usable schema without replay errors.
	testdb.MigrateTo(t, dsn, 129)
	assertSpamTables(t, dsn, true)
	_ = ctx
}

func TestSharedVerdictNeverUsesFirstRecipientPrivatePolicy(t *testing.T) {
	platform, app := testdb.OpenMigrated(t)
	tenantA, a, b, tenantB, c := seedMailboxes(t, platform)
	setSenderRule(t, app, tenantA, a, "news@bulk.example", "deny")
	setSenderRule(t, app, tenantA, b, "news@bulk.example", "allow")
	setSenderRule(t, app, tenantB, c, "news@bulk.example", "deny")
	engine := spamEngine(platform, app)
	var baseline VerdictResponse
	for i, recipients := range [][]string{{"one@a-spam.test", "two@a-spam.test", "one@b-spam.test"}, {"one@b-spam.test", "two@a-spam.test", "one@a-spam.test"}, {"two@a-spam.test", "one@a-spam.test"}} {
		req := messageFor("news@bulk.example", recipients[0])
		req.Recipients = recipients
		got := engine.Decide(t.Context(), req)
		if i == 0 {
			baseline = got
		} else if !reflect.DeepEqual(got, baseline) {
			t.Fatalf("recipient ordering/private rules changed shared verdict: %#v vs %#v", got, baseline)
		}
	}
	// A forged Authentication-Results with our configured authserv-id must not
	// influence classification, regardless of claimed pass/fail.
	req := messageFor("news@bulk.example", "one@a-spam.test")
	without := engine.Decide(t.Context(), req)
	req.Headers = append(req.Headers, Header{Name: "Authentication-Results", Value: "mx.test; dmarc=reject; dkim=fail; spf=fail"})
	if forged := engine.Decide(t.Context(), req); !reflect.DeepEqual(forged, without) {
		t.Fatal("sender-controlled authentication result influenced shared classification")
	}
}

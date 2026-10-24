package miabimport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/miadbinventory"
	"github.com/bushixiucai/urmail/internal/testdb"
)

const syntheticSource = `import sqlite3,sys
c=sqlite3.connect(sys.argv[1])
c.executescript("""CREATE TABLE users(id INTEGER PRIMARY KEY AUTOINCREMENT,email TEXT NOT NULL UNIQUE,password TEXT NOT NULL,extra,privileges TEXT NOT NULL DEFAULT '',quota TEXT NOT NULL DEFAULT '0');
CREATE TABLE aliases(id INTEGER PRIMARY KEY AUTOINCREMENT,source TEXT NOT NULL UNIQUE,destination TEXT NOT NULL,permitted_senders TEXT);
CREATE TABLE auto_aliases(id INTEGER PRIMARY KEY AUTOINCREMENT,source TEXT NOT NULL UNIQUE,destination TEXT NOT NULL,permitted_senders TEXT);
CREATE TABLE mfa(id INTEGER PRIMARY KEY AUTOINCREMENT,user_id INTEGER NOT NULL,type TEXT NOT NULL,secret TEXT NOT NULL,mru_token TEXT,label TEXT,FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE);""")
users=[]
for d,count in enumerate([12,25,3,1,1,1]):
 for i in range(count):
  address='user'+str(len(users))+'@d'+str(d)+'.legacy.test';users.append(address)
  c.execute('INSERT INTO users(email,password,privileges) VALUES(?,?,?)',(address,'{SHA512-CRYPT}$6$fixturesalt$'+'A'*86,'admin' if len(users)==1 else ('管理员' if len(users)==7 else '')))
for table,source,target in [('aliases','admin@d1.legacy.test',users[12]),('aliases','hostmaster@d0.legacy.test',users[12]),('auto_aliases','admin@d1.legacy.test','postmaster@box.legacy.test'),('auto_aliases','postmaster@box.legacy.test',users[12])]:
 c.execute('INSERT INTO '+table+'(source,destination) VALUES(?,?)',(source,target))
for i in range(20):c.execute('INSERT INTO auto_aliases(source,destination) VALUES(?,?)',('system'+str(i)+'@box.legacy.test',users[i]))
c.commit();c.close()`

type coreFixture struct {
	pool       *db.Pool
	source     *miadbinventory.ReviewedSource
	options    Options
	configPath string
}

func newCoreFixture(t *testing.T) coreFixture {
	t.Helper()
	dir := t.TempDir()
	input := miadbinventory.Options{ConfigPath: filepath.Join(dir, "mailinabox.conf"), SQLitePath: filepath.Join(dir, "users.sqlite"), MappingPath: filepath.Join(dir, "mapping.json")}
	if err := os.WriteFile(input.ConfigPath, []byte("STORAGE_ROOT=/home/user-data\nPRIMARY_HOSTNAME=box.legacy.test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := exec.CommandContext(t.Context(), "/usr/bin/python3", "-I", "-B", "-c", syntheticSource, input.SQLitePath).Run(); err != nil {
		t.Fatal("create synthetic legacy fixture")
	}
	domains := []map[string]string{}
	for _, name := range []string{"d0.legacy.test", "d1.legacy.test", "d2.legacy.test", "d3.legacy.test", "d4.legacy.test", "d5.legacy.test", "box.legacy.test"} {
		domains = append(domains, map[string]string{"domain": name, "tenant_ref": "legacy-primary"})
	}
	mapping := map[string]any{"version": 1, "domains": domains}
	writeMapping := func() {
		t.Helper()
		raw, _ := json.Marshal(mapping)
		if err := os.WriteFile(input.MappingPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeMapping()
	report, err := miadbinventory.Inspect(t.Context(), input)
	if err != nil || report.SnapshotSHA256 == "" {
		t.Fatal("inspect synthetic source", err)
	}
	mapping["version"] = 2
	mapping["reviewed_snapshot_sha256"] = report.SnapshotSHA256
	mapping["legacy_privileges"] = "preserve_metadata_no_authority"
	mapping["allow_same_tenant_cross_domain_aliases"] = true
	mapping["allow_equivalent_alias_dedup"] = true
	writeMapping()
	source, err := miadbinventory.LoadReviewedSource(t.Context(), miadbinventory.ReviewedSourceOptions{Options: input, ExpectedSnapshotSHA256: report.SnapshotSHA256})
	if err != nil {
		t.Fatal(err)
	}
	return newCoreTarget(t, source, input.ConfigPath)
}

func newCoreTarget(t *testing.T, source *miadbinventory.ReviewedSource, configPath string) coreFixture {
	t.Helper()
	var err error
	dsn := testdb.New(t)
	admin, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal("owned fixture privacy setup failed")
	}
	var database string
	if err = admin.QueryRow(t.Context(), "SELECT current_database()").Scan(&database); err != nil {
		t.Fatal("owned fixture identity failed")
	}
	for _, setting := range []string{"log_error_verbosity=terse", "log_min_error_statement=panic", "log_min_messages=fatal", "log_statement=none", "log_min_duration_statement=-1", "log_parameter_max_length=0", "log_parameter_max_length_on_error=0"} {
		if _, err = admin.Exec(t.Context(), "ALTER DATABASE "+pgx.Identifier{database}.Sanitize()+" SET "+setting); err != nil {
			t.Fatal("owned fixture privacy policy failed")
		}
	}
	_ = admin.Close(t.Context())
	m, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	pool, err := db.OpenPlatform(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	scalar := func(query string, args ...any) int64 {
		t.Helper()
		var id int64
		if err = pool.QueryRow(t.Context(), query, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('legacy-core','Legacy fixture') RETURNING id")
	actor := scalar("INSERT INTO users(email,password_hash) VALUES('operator@fixture.test','unused') RETURNING id")
	if _, err = pool.Exec(t.Context(), "INSERT INTO platform_admins(user_id) VALUES($1)", actor); err != nil {
		t.Fatal(err)
	}
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Legacy fixture','legacy-core','active') RETURNING id", brand)
	return coreFixture{pool, source, Options{TenantID: tenant, ActorID: actor, TenantRef: "legacy-primary", Reason: "Reviewed source snapshot and preserved service mapping"}, configPath}
}

func TestApplyCorePreserves43CredentialsAndExplicitSenderAuthority(t *testing.T) {
	f := newCoreFixture(t)
	result, err := ApplyCore(t.Context(), f.pool, f.source, f.options)
	if err != nil || !result.Applied || result.CommitOutcome != "committed" || !result.CoreOnly || !result.SievePending || result.CutoverReady || result.Mailboxes != 43 || result.Domains != 7 || result.Aliases != 23 {
		t.Fatal("core import result", result, err)
	}
	for _, m := range f.source.Mailboxes() {
		var hash string
		var quota int64
		if err = f.pool.QueryRow(t.Context(), "SELECT password_hash,quota_bytes FROM mailboxes WHERE tenant_id=$1 AND address=$2", f.options.TenantID, m.Address).Scan(&hash, &quota); err != nil || hash != m.PasswordHash || quota != -1 {
			t.Fatal("credential/quota bytes changed")
		}
	}
	checks := []string{
		`SELECT count(*)=7 AND bool_and(status='pending_dns' AND verified_at IS NULL AND provisioning_state<>'ready') FROM domains WHERE tenant_id=$1`,
		`SELECT bool_and(NOT urmail_domain_mail_ready(tenant_id,id)) FROM domains WHERE tenant_id=$1`,
		`SELECT count(*)=1 AND bool_and(entitlement_source='migration_legacy' AND period_end IS NULL) FROM subscriptions WHERE tenant_id=$1 AND status='active'`,
		`SELECT count(*)=2 FROM mailbox_aliases WHERE tenant_id=$1 AND cardinality(sender_logins)=1`,
		`SELECT count(*)=21 FROM mailbox_aliases WHERE tenant_id=$1 AND cardinality(sender_logins)=0`,
		`SELECT (SELECT count(*) FROM orders WHERE tenant_id=$1)+(SELECT count(*) FROM payments WHERE tenant_id=$1)+(SELECT count(*) FROM subscription_terms WHERE tenant_id=$1)+(SELECT count(*) FROM billing_calendars WHERE tenant_id=$1)+(SELECT count(*) FROM tenant_trials WHERE tenant_id=$1)=0`,
		`SELECT count(*)=1 AND bool_and(detail->>'commit_outcome'='committed' AND (detail->>'core_only')::boolean AND NOT (detail->>'cutover_ready')::boolean) FROM audit_logs WHERE tenant_id=$1 AND action='IMPORT legacy.mail'`,
	}
	for index, query := range checks {
		var ok bool
		if err = f.pool.QueryRow(t.Context(), query, f.options.TenantID).Scan(&ok); err != nil || !ok {
			var facts string
			_ = f.pool.QueryRow(t.Context(), "SELECT jsonb_build_object('count',count(*),'pending',count(*) FILTER(WHERE status='pending_dns'),'verified',count(*) FILTER(WHERE verified_at IS NOT NULL),'ready',count(*) FILTER(WHERE provisioning_state='ready'))::text FROM domains WHERE tenant_id=$1", f.options.TenantID).Scan(&facts)
			t.Fatalf("core invariant %d failed: %v safe domain facts=%s", index, err, facts)
		}
	}
	var audit string
	if err = f.pool.QueryRow(t.Context(), "SELECT jsonb_agg(detail)::text FROM audit_logs WHERE tenant_id=$1", f.options.TenantID).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(audit, "$6$") || strings.Contains(audit, "user12@") || strings.Contains(audit, "管理员") {
		t.Fatal("private source fields escaped to audit")
	}
	if _, err = ApplyCore(t.Context(), f.pool, f.source, f.options); !errors.Is(err, ErrTarget) {
		t.Fatal("nonempty replay was not refused", err)
	}
}

func TestApplyCoreAuditFailureRollsBackGrantAndEveryResource(t *testing.T) {
	f := newCoreFixture(t)
	_, err := f.pool.Exec(t.Context(), `CREATE FUNCTION reject_core_import_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='IMPORT legacy.mail' THEN RAISE EXCEPTION 'FIXTURE_AUDIT_FAILURE';END IF; RETURN NEW;END $$;CREATE TRIGGER reject_core_import_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_core_import_audit()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyCore(t.Context(), f.pool, f.source, f.options); !errors.Is(err, ErrRejected) {
		t.Fatal("audit failure ignored", err)
	}
	var n int
	if err = f.pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM domains WHERE tenant_id=$1)+(SELECT count(*) FROM mailboxes WHERE tenant_id=$1)+(SELECT count(*) FROM mailbox_aliases WHERE tenant_id=$1)+(SELECT count(*) FROM subscriptions WHERE tenant_id=$1 AND entitlement_source='migration_legacy')+(SELECT count(*) FROM audit_logs WHERE tenant_id=$1)", f.options.TenantID).Scan(&n); err != nil || n != 0 {
		t.Fatal("failed import left committed facts", err)
	}
}

func TestApplyCoreRefusesChangedSourceAndWrongTenantMapping(t *testing.T) {
	f := newCoreFixture(t)
	wrong := f.options
	wrong.TenantRef = "unapproved-group"
	if _, err := ApplyCore(t.Context(), f.pool, f.source, wrong); !errors.Is(err, ErrSource) {
		t.Fatal("mapping was guessed", err)
	}
	if err := os.WriteFile(f.configPath, []byte("STORAGE_ROOT=/changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyCore(t.Context(), f.pool, f.source, f.options); !errors.Is(err, ErrSource) {
		t.Fatal("source drift accepted", err)
	}
}

func TestApplyCoreConcurrentImportNeverDuplicates(t *testing.T) {
	f := newCoreFixture(t)
	outcomes := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ApplyCore(context.Background(), f.pool, f.source, f.options)
			outcomes <- err
		}()
	}
	wg.Wait()
	close(outcomes)
	passed := 0
	for err := range outcomes {
		if err == nil {
			passed++
		}
	}
	if passed != 1 {
		t.Fatal("concurrent imports did not produce one committed result")
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), "SELECT count(*) FROM mailboxes WHERE tenant_id=$1", f.options.TenantID).Scan(&count); err != nil || count != 43 {
		t.Fatal("concurrent import duplicated resources", err)
	}
}

// This opt-in rehearsal consumes only explicit private snapshots and always
// creates/drops its own PostgreSQL fixture. No production target is accepted.
func TestApplyRealPrivateCoreSourceRehearsal(t *testing.T) {
	directory := os.Getenv("URMAIL_M8_PRIVATE_CORE_SOURCE")
	if directory == "" {
		t.Skip("private source rehearsal not requested")
	}
	input := miadbinventory.Options{ConfigPath: filepath.Join(directory, "mailinabox.conf"), SQLitePath: filepath.Join(directory, "users.sqlite"), MappingPath: filepath.Join(directory, "mapping.json")}
	source, err := miadbinventory.LoadReviewedSource(t.Context(), miadbinventory.ReviewedSourceOptions{Options: input, ExpectedSnapshotSHA256: "2f8f901cab694c68757a5fe8f8ad282d4f52a0e2e1e80d5dbafb7a98e7218c3f"})
	if err != nil {
		t.Fatal("private source review refused; no values printed")
	}
	f := newCoreTarget(t, source, input.ConfigPath)
	result, err := ApplyCore(t.Context(), f.pool, source, f.options)
	if err != nil || !result.Applied || result.CutoverReady || result.Mailboxes != 43 || result.Domains != 7 {
		t.Fatal("real private metadata rehearsal failed", result, err)
	}
	for index, mailbox := range source.Mailboxes() {
		var hash string
		var quota int64
		if err = f.pool.QueryRow(t.Context(), "SELECT password_hash,quota_bytes FROM mailboxes WHERE tenant_id=$1 AND address=$2", f.options.TenantID, mailbox.Address).Scan(&hash, &quota); err != nil || hash != mailbox.PasswordHash || quota != mailbox.QuotaBytes {
			t.Fatalf("private credential/quota mismatch at source row %d", index)
		}
	}
	if source.ValidateUnchanged(t.Context()) != nil {
		t.Fatal("private source changed")
	}
	t.Log("43 credentials and quotas preserved; seven unverified domains; core-only, activation false; owned fixture cleanup required")
}

func TestApplyCoreRefusesStandaloneInvoiceHistory(t *testing.T) {
	f := newCoreFixture(t)
	if _, err := f.pool.Exec(t.Context(), "INSERT INTO invoices(tenant_id,number,amount_cents,currency,status) VALUES($1,'LEGACY-IMPORT-FIXTURE',100,'USD','draft')", f.options.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyCore(t.Context(), f.pool, f.source, f.options); !errors.Is(err, ErrTarget) {
		t.Fatal("standalone financial history was ignored", err)
	}
}

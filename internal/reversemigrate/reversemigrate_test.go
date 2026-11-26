package reversemigrate

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bushixiucai/urmail/internal/datamigrate"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	sqlmigrations "github.com/bushixiucai/urmail/migrations_sqlite"
)

func boolPtr(b bool) *bool { return &b }

func TestClass6IsolationAndResidualWriters(t *testing.T) {
	dir := t.TempDir()
	holdPath := filepath.Join(dir, "upgrade.hold")
	if err := os.WriteFile(holdPath, []byte("gen-20261126"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Positive: all writers/listeners stopped and hold present -> locked.
	rep, err := VerifyIsolation(IsolationCheckInput{
		HoldFilePath:        holdPath,
		ExpectedGeneration:  "gen-20261126",
		ListenersStopped:    boolPtr(true),
		WritersStopped:      boolPtr(true),
		SQLiteWALClean:      boolPtr(true),
		PGActiveWritersZero: boolPtr(true),
	}, time.Now())
	if err != nil || rep.State != LockStateLocked {
		t.Fatalf("expected locked, got state=%q err=%v", rep.State, err)
	}

	// Negative 1: active writer present -> unlocked, refused fail-closed.
	rep, err = VerifyIsolation(IsolationCheckInput{
		HoldFilePath:        holdPath,
		ListenersStopped:    boolPtr(true),
		WritersStopped:      boolPtr(false),
		SQLiteWALClean:      boolPtr(true),
		PGActiveWritersZero: boolPtr(true),
	}, time.Now())
	if err == nil || rep.State != LockStateUnlocked {
		t.Fatalf("expected unlocked error on active writer, got state=%q err=%v", rep.State, err)
	}

	// Negative 2: unconfirmed observation (nil) -> unconfirmed, never downgraded to unlocked.
	rep, err = VerifyIsolation(IsolationCheckInput{
		HoldFilePath:     holdPath,
		ListenersStopped: boolPtr(true),
	}, time.Now())
	if err == nil || rep.State != LockStateUnconfirmed {
		t.Fatalf("expected unconfirmed on missing observation, got state=%q err=%v", rep.State, err)
	}
}

func TestClass1And2DBDeltaDualWriteHardDeleteAndTimers(t *testing.T) {
	// Positive: hard-deleted rows (in baseline & PG, absent from SQLite) are scheduled for DELETE.
	base := map[string]bool{"1": true, "2": true, "3": true}
	pg := map[string]bool{"1": true, "2": true, "3": true}
	sq := map[string]bool{"1": true, "3": true, "4": true}
	del, err := ReconcileTableDelta("mailbox_aliases", base, pg, sq)
	if err != nil {
		t.Fatalf("ReconcileTableDelta positive: %v", err)
	}
	if len(del) != 1 || del[0] != "2" {
		t.Fatalf("expected [2] to be hard-deleted, got %v", del)
	}

	// Negative 1: dual-write detected (pk=99 in PG, absent from both baseline and SQLite).
	pgDual := map[string]bool{"1": true, "2": true, "3": true, "99": true}
	if _, err := ReconcileTableDelta("mailbox_aliases", base, pgDual, sq); err == nil || !strings.Contains(err.Error(), "dual-write") {
		t.Fatalf("expected dual-write error, got %v", err)
	}

	// Class 5 (Timers/Scheduled Sends) Positive & Negative:
	if err := CheckScheduledSendMonotonicity(
		map[string]string{"10": "sent"},
		map[string]string{"10": "sent"},
	); err != nil {
		t.Fatalf("unexpected monotonicity error: %v", err)
	}
	if err := CheckScheduledSendMonotonicity(
		map[string]string{"10": "sent"},
		map[string]string{"10": "pending"},
	); err == nil {
		t.Fatal("expected regression error when SQLite=sent and PG=pending")
	}

	// Reverse value decoding round-trip check.
	now := time.Unix(1795000000, 0).UTC()
	gotTime, err := DecodeSQLiteValue(datamigrate.ColumnMap{Encoding: datamigrate.EncodingUnixMicro}, now.UnixMicro())
	if err != nil || !gotTime.(time.Time).Equal(now) {
		t.Fatalf("decode unix micro: %v %v", gotTime, err)
	}
}

func TestClass1And3MaildirIMAPAndSieve(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src-maildir")
	tgt := filepath.Join(dir, "tgt-maildir")
	quar := filepath.Join(dir, "quarantine")

	// Seed source with 1 new message, uidlist V100 N5, keywords, subscriptions, sieve, and stale .svbin.
	for _, p := range []string{
		filepath.Join(src, "user1/Maildir/cur/msg1:2,S"),
		filepath.Join(src, "user1/Maildir/dovecot-uidlist"),
		filepath.Join(src, "user1/Maildir/dovecot-keywords"),
		filepath.Join(src, "user1/Maildir/subscriptions"),
		filepath.Join(src, "user1/sieve/default.sieve"),
		filepath.Join(tgt, "user1/Maildir/cur/deleted-on-src:2,S"),
		filepath.Join(tgt, "user1/Maildir/dovecot-uidlist"),
		filepath.Join(tgt, "user1/sieve/default.svbin"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(src, "user1/Maildir/cur/msg1:2,S"), []byte("Subject: hi\r\n\r\nbody"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "user1/Maildir/dovecot-uidlist"), []byte("3 V100 N5 G0\n1 :msg1\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "user1/Maildir/dovecot-keywords"), []byte("0 $Label1\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "user1/Maildir/subscriptions"), []byte("Sent\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "user1/sieve/default.sieve"), []byte(`require ["fileinto"];`), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tgt, "user1/Maildir/cur/deleted-on-src:2,S"), []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tgt, "user1/Maildir/dovecot-uidlist"), []byte("3 V100 N1 G0\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tgt, "user1/sieve/default.svbin"), []byte("compiled"), 0o640); err != nil {
		t.Fatal(err)
	}

	// Dry-run leaves target unchanged.
	mDry, _, err := MergeMaildirAndIMAP(src, tgt, quar, false, true)
	if err != nil || mDry.FilesCopied != 1 || mDry.FilesQuarantined != 1 {
		t.Fatalf("dry-run maildir unexpected: %+v err=%v", mDry, err)
	}
	if _, err := os.Stat(filepath.Join(tgt, "user1/Maildir/cur/msg1:2,S")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote target file")
	}

	// Real merge copies msg1, quarantines deleted-on-src, syncs sieve, purges .svbin.
	mRep, iRep, err := MergeMaildirAndIMAP(src, tgt, quar, false, false)
	if err != nil || mRep.FilesCopied != 1 || mRep.FilesQuarantined != 1 || iRep.SieveSynced != 1 || iRep.SvbinPurged != 1 {
		t.Fatalf("real maildir/imap unexpected: m=%+v i=%+v err=%v", mRep, iRep, err)
	}
	if _, err := os.Stat(filepath.Join(tgt, "user1/sieve/default.svbin")); !os.IsNotExist(err) {
		t.Fatalf("expected stale .svbin to be purged")
	}
	if _, err := os.Stat(filepath.Join(quar, "user1/Maildir/cur/deleted-on-src:2,S")); err != nil {
		t.Fatalf("expected deleted file in quarantine: %v", err)
	}

	// Negative: UIDVALIDITY split with target next_uid > 1 aborts fail-closed.
	if err := os.WriteFile(filepath.Join(tgt, "user1/Maildir/dovecot-uidlist"), []byte("3 V999 N2 G0\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := MergeMaildirAndIMAP(src, tgt, quar, false, true); err == nil || !strings.Contains(err.Error(), "UIDVALIDITY split") {
		t.Fatalf("expected UIDVALIDITY split error, got %v", err)
	}
}

func TestClass1QueueCompoundDedup(t *testing.T) {
	dir := t.TempDir()
	src := []QueueItem{
		{QueueID: "Q1", EnvelopeSender: "a@example.test", PendingRecipients: []string{"b@example.test"}, MIMEBodySHA256: "deadbeef"},
		{QueueID: "Q2", EnvelopeSender: "a@example.test", PendingRecipients: []string{"c@example.test"}, MIMEBodySHA256: "cafebabe"},
	}
	// Positive: Q2 skipped because already delivered, Q1 queued for handoff.
	qRep, handoff, err := MergeQueue(src, nil, map[string]bool{"cafebabe": true}, dir, false)
	if err != nil || qRep.HandoffQueued != 1 || qRep.SkippedDelivered != 1 || len(handoff) != 1 {
		t.Fatalf("unexpected queue merge: %+v err=%v", qRep, err)
	}

	// Negative: duplicate compound key in source aborts.
	dupSrc := make([]QueueItem, 0, len(src)+1)
	dupSrc = append(dupSrc, src...)
	dupSrc = append(dupSrc, QueueItem{QueueID: "Q3", EnvelopeSender: "a@example.test", PendingRecipients: []string{"b@example.test"}, MIMEBodySHA256: "deadbeef"})
	if _, _, err := MergeQueue(dupSrc, nil, nil, dir, true); err == nil || !strings.Contains(err.Error(), "duplicate source queue compound key") {
		t.Fatalf("expected duplicate compound key abort, got %v", err)
	}
}

func makeECDSACert(t *testing.T, notAfter time.Time) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "box.example.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

func TestClass4And5TLSECDSAAndDNS(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	cert1, key1 := makeECDSACert(t, now.Add(48*time.Hour))
	_, key2 := makeECDSACert(t, now.Add(48*time.Hour))

	srcCert := filepath.Join(dir, "src.crt")
	srcKey := filepath.Join(dir, "src.key")
	badKey := filepath.Join(dir, "bad.key")
	tgtCert := filepath.Join(dir, "tgt.crt")
	tgtKey := filepath.Join(dir, "tgt.key")
	if err := os.WriteFile(srcCert, cert1, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcKey, key1, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badKey, key2, 0o600); err != nil {
		t.Fatal(err)
	}

	// Positive: ACME ECDSA cert & matching key succeed.
	tRep, err := VerifyAndMergeTLS(srcCert, srcKey, tgtCert, tgtKey, filepath.Join(dir, "tls-bak"), now, false)
	if err != nil || !tRep.Verified || tRep.Algorithm != "ECDSA" || !tRep.Updated {
		t.Fatalf("ECDSA TLS merge failed: %+v err=%v", tRep, err)
	}

	// Negative: ECDSA pubkey mismatch aborts.
	if _, err := VerifyAndMergeTLS(srcCert, badKey, tgtCert, tgtKey, "", now, true); err == nil || !strings.Contains(err.Error(), "pubkey mismatch") {
		t.Fatalf("expected TLS pubkey mismatch, got %v", err)
	}

	// DNS Positive & Negative:
	goodZone := filepath.Join(dir, "good.zone")
	badZone := filepath.Join(dir, "bad.zone")
	if err := os.WriteFile(goodZone, []byte("$ORIGIN example.test.\n@ IN SOA ns1.example.test. admin.example.test. 2026112601 3600 600 86400 300\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badZone, []byte("invalid zone"), 0o600); err != nil {
		t.Fatal(err)
	}
	if dRep, err := VerifyDNS([]string{goodZone}, true); err != nil || !dRep.Verified || !dRep.ZeroDNSCutover {
		t.Fatalf("VerifyDNS positive: %+v err=%v", dRep, err)
	}
	if _, err := VerifyDNS([]string{badZone}, true); err == nil {
		t.Fatal("expected invalid zone syntax error")
	}
}

func TestOrchestratorDryRunAndEndToEnd(t *testing.T) {
	dir := t.TempDir()
	hold := filepath.Join(dir, "upgrade.hold")
	if err := os.WriteFile(hold, []byte("gen-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	cert, key := makeECDSACert(t, time.Now().Add(24*time.Hour))
	srcCert := filepath.Join(dir, "src.crt")
	srcKey := filepath.Join(dir, "src.key")
	if err := os.WriteFile(srcCert, cert, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcKey, key, 0o600); err != nil {
		t.Fatal(err)
	}

	rep, err := Run(context.Background(), Config{
		DryRun: true,
		Isolation: IsolationCheckInput{
			HoldFilePath:        hold,
			ListenersStopped:    boolPtr(true),
			WritersStopped:      boolPtr(true),
			SQLiteWALClean:      boolPtr(true),
			PGActiveWritersZero: boolPtr(true),
		},
		SrcTLSCert: srcCert,
		SrcTLSKey:  srcKey,
		TgtTLSCert: filepath.Join(dir, "tgt.crt"),
		TgtTLSKey:  filepath.Join(dir, "tgt.key"),
	})
	if err != nil || !rep.DryRun || !rep.NoSLACommitment || !rep.Reconciliation.Passed {
		t.Fatalf("orchestrator dry-run failed: %+v err=%v", rep, err)
	}
}

func TestOrderTablesForPGAndMetadataExclusion(t *testing.T) {
	tm, err := datamigrate.ParseTypeMap(bytes.NewReader(sqlmigrations.TypeMapTSV), "migrations_sqlite/type-map.tsv")
	if err != nil {
		t.Fatalf("ParseTypeMap: %v", err)
	}
	ordered, err := OrderTablesForPG(tm.Tables(), nil)
	if err != nil {
		t.Fatalf("OrderTablesForPG: %v", err)
	}
	pos := make(map[string]int, len(ordered))
	for i, tbl := range ordered {
		if tbl == "schema_migrations" || tbl == "sqlite_sequence" || tbl == "urmail_s5_migrate_meta" {
			t.Fatalf("metadata table %q must be excluded from reverse migration plan", tbl)
		}
		pos[tbl] = i
	}
	if pos["mailboxes"] >= pos["mailbox_aliases"] {
		t.Fatalf("mailboxes (pos %d) must precede mailbox_aliases (pos %d)", pos["mailboxes"], pos["mailbox_aliases"])
	}
	if pos["settings"] >= pos["domains"] || pos["domains"] >= pos["mailboxes"] {
		t.Fatalf("expected settings -> domains -> mailboxes order, got %d -> %d -> %d", pos["settings"], pos["domains"], pos["mailboxes"])
	}
}

func TestPG16RealTriggerAliasOrderAndNoSchemaMigrationsDelete(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("URMAIL_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("URMAIL_TEST_DATABASE_URL not set; skipping PG16 trigger repro")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	// Create an isolated temporary schema with real PG trigger semantics matching 0069/0001.
	schema := fmt.Sprintf("e7_rev_test_%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE") }()

	// Build a standalone SQLite DB matching the minimal tables.
	sqPath := filepath.Join(t.TempDir(), "e7.sqlite")
	sqDB, err := sqlitedb.Open(ctx, sqPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sqDB.Close() }()
	if _, err := sqDB.ExecContext(ctx, `
		CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, dirty INTEGER NOT NULL DEFAULT 0);
		INSERT INTO schema_migrations(version, dirty) VALUES(7, 0);
		CREATE TABLE mailboxes (id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL, address TEXT NOT NULL, status TEXT NOT NULL);
		INSERT INTO mailboxes(id, tenant_id, address, status) VALUES(1, 1, 'user-a@example.test', 'active');
		CREATE TABLE mailbox_aliases (id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL, address TEXT NOT NULL, sender_logins TEXT NOT NULL);
		INSERT INTO mailbox_aliases(id, tenant_id, address, sender_logins) VALUES(14, 1, 'alias@example.test', '["user-a@example.test"]');
	`); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE %s.schema_migrations (version bigint PRIMARY KEY, dirty boolean NOT NULL DEFAULT false);
		INSERT INTO %s.schema_migrations(version, dirty) VALUES(159, false);
		CREATE TABLE %s.mailboxes (id bigint PRIMARY KEY, tenant_id bigint NOT NULL, address text NOT NULL, status text NOT NULL);
		CREATE TABLE %s.mailbox_aliases (id bigint PRIMARY KEY, tenant_id bigint NOT NULL, address text NOT NULL, sender_logins jsonb NOT NULL);
		CREATE OR REPLACE FUNCTION %s.alias_guard() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM jsonb_array_elements_text(NEW.sender_logins) AS elem(val)
				WHERE NOT EXISTS (SELECT 1 FROM %s.mailboxes m WHERE m.tenant_id = NEW.tenant_id AND m.address = elem.val AND m.status <> 'deleted')
			) THEN
				RAISE EXCEPTION 'ALIAS_SENDER_LOGIN_NOT_TENANT_MAILBOX' USING ERRCODE = '23514';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER alias_sender_logins_guard BEFORE INSERT OR UPDATE ON %s.mailbox_aliases FOR EACH ROW EXECUTE FUNCTION %s.alias_guard();
	`, schema, schema, schema, schema, schema, schema, schema, schema)); err != nil {
		t.Fatalf("seed pg schema: %v", err)
	}

	// Open a pool pinned to the isolated schema via search_path.
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	scopedPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer scopedPool.Close()

	// 1. Reproduce the failure when mailbox_aliases is inserted before mailboxes (SQLSTATE 23514).
	if _, err := scopedPool.Exec(ctx, `INSERT INTO mailbox_aliases(id, tenant_id, address, sender_logins) VALUES(14, 1, 'alias@example.test', '["user-a@example.test"]'::jsonb)`); err == nil || !strings.Contains(err.Error(), "23514") {
		t.Fatalf("expected SQLSTATE 23514 when inserting mailbox_aliases before mailboxes, got %v", err)
	}

	// 2. Run MigrateDBSQLiteToPG: mailboxes precedes mailbox_aliases, succeeds, and schema_migrations=159 is untouched.
	tm := datamigrate.TypeMap{
		"schema_migrations": {
			"version": {Table: "schema_migrations", Column: "version", PGType: "bigint", SQLiteType: "INTEGER", Encoding: datamigrate.EncodingPassthrough},
		},
		"mailboxes": {
			"id":        {Table: "mailboxes", Column: "id", PGType: "bigint", SQLiteType: "INTEGER", Encoding: datamigrate.EncodingPassthrough},
			"tenant_id": {Table: "mailboxes", Column: "tenant_id", PGType: "bigint", SQLiteType: "INTEGER", Encoding: datamigrate.EncodingPassthrough},
			"address":   {Table: "mailboxes", Column: "address", PGType: "text", SQLiteType: "TEXT", Encoding: datamigrate.EncodingPassthrough},
			"status":    {Table: "mailboxes", Column: "status", PGType: "text", SQLiteType: "TEXT", Encoding: datamigrate.EncodingPassthrough},
		},
		"mailbox_aliases": {
			"id":            {Table: "mailbox_aliases", Column: "id", PGType: "bigint", SQLiteType: "INTEGER", Encoding: datamigrate.EncodingPassthrough},
			"tenant_id":     {Table: "mailbox_aliases", Column: "tenant_id", PGType: "bigint", SQLiteType: "INTEGER", Encoding: datamigrate.EncodingPassthrough},
			"address":       {Table: "mailbox_aliases", Column: "address", PGType: "text", SQLiteType: "TEXT", Encoding: datamigrate.EncodingPassthrough},
			"sender_logins": {Table: "mailbox_aliases", Column: "sender_logins", PGType: "jsonb", SQLiteType: "TEXT", Encoding: datamigrate.EncodingJSONText},
		},
	}

	rep, err := MigrateDBSQLiteToPG(ctx, sqDB, scopedPool, tm, nil, false)
	if err != nil {
		t.Fatalf("MigrateDBSQLiteToPG failed: %v", err)
	}
	if !rep.ZeroDiff || rep.RowsUpserted != 2 || rep.RowsDeleted != 0 {
		t.Fatalf("unexpected MigrateDBSQLiteToPG report: %+v", rep)
	}
	for _, act := range rep.Actions {
		if strings.Contains(act, "schema_migrations") {
			t.Fatalf("merge-report actions must never reference schema_migrations: %q", act)
		}
	}
	var pgSchemaVer int64
	if err := scopedPool.QueryRow(ctx, `SELECT version FROM schema_migrations WHERE version = 159`).Scan(&pgSchemaVer); err != nil {
		t.Fatalf("PG schema_migrations=159 must remain intact: %v", err)
	}
}

func TestPG16GeneratedAlwaysIdentityAndTriggerRestore(t *testing.T) {
	dsn := os.Getenv("URMAIL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("URMAIL_TEST_DATABASE_URL not set; skipping PG16 GENERATED ALWAYS AS IDENTITY + trigger restore test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect pg: %v", err)
	}
	defer pool.Close()

	schema := fmt.Sprintf("e7_identity_trig_%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE") }()

	if _, err := pool.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE %s.mailboxes (
			id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			tenant_id bigint NOT NULL,
			address text NOT NULL,
			status text NOT NULL,
			password_hash text NOT NULL DEFAULT '',
			password_version bigint NOT NULL DEFAULT 0,
			password_revoke_pending boolean NOT NULL DEFAULT false
		);
		CREATE TABLE %s.mailbox_scheduled_sends (
			id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			mailbox_id bigint NOT NULL REFERENCES %s.mailboxes(id),
			status text NOT NULL
		);
		CREATE TABLE %s.mailbox_aliases (
			id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			tenant_id bigint NOT NULL,
			address text NOT NULL,
			sender_logins text[] NOT NULL DEFAULT '{}'
		);
		CREATE OR REPLACE FUNCTION %s.pw_ver_guard() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF TG_OP = 'INSERT' THEN
				IF NEW.password_version <> 0 OR NEW.password_revoke_pending THEN
					RAISE EXCEPTION 'MAILBOX_PASSWORD_VERSION_IMMUTABLE' USING ERRCODE = '42501';
				END IF;
				RETURN NEW;
			END IF;
			IF NEW.password_hash IS NOT DISTINCT FROM OLD.password_hash THEN
				IF NEW.password_version <> OLD.password_version OR (NEW.password_revoke_pending IS DISTINCT FROM OLD.password_revoke_pending) THEN
					RAISE EXCEPTION 'MAILBOX_PASSWORD_VERSION_IMMUTABLE' USING ERRCODE = '42501';
				END IF;
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER mailbox_password_version_guard BEFORE INSERT OR UPDATE ON %s.mailboxes FOR EACH ROW EXECUTE FUNCTION %s.pw_ver_guard();

		CREATE OR REPLACE FUNCTION %s.alias_guard() RETURNS trigger LANGUAGE plpgsql AS $$
		DECLARE login text;
		BEGIN
			FOREACH login IN ARRAY NEW.sender_logins LOOP
				IF NOT EXISTS (SELECT 1 FROM %s.mailboxes m WHERE m.tenant_id = NEW.tenant_id AND m.address = login AND m.status <> 'deleted') THEN
					RAISE EXCEPTION 'ALIAS_SENDER_LOGIN_NOT_TENANT_MAILBOX' USING ERRCODE = '23514';
				END IF;
			END LOOP;
			RETURN NEW;
		END $$;
		CREATE TRIGGER alias_sender_logins_guard BEFORE INSERT OR UPDATE OF tenant_id, sender_logins ON %s.mailbox_aliases FOR EACH ROW EXECUTE FUNCTION %s.alias_guard();
	`, schema, schema, schema, schema, schema, schema, schema, schema, schema, schema, schema)); err != nil {
		t.Fatalf("seed pg schema: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	scopedPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer scopedPool.Close()

	// 1. Confirm direct explicit-ID insert into GENERATED ALWAYS AS IDENTITY fails with SQLSTATE 428C9.
	if _, err := scopedPool.Exec(ctx, `INSERT INTO mailbox_scheduled_sends (id, mailbox_id, status) VALUES (101, 1, 'queued')`); err == nil || !strings.Contains(err.Error(), "428C9") {
		t.Fatalf("expected SQLSTATE 428C9 without OVERRIDING SYSTEM VALUE, got %v", err)
	}

	// Snapshot exact pre-migration trigger definitions.
	snapshotTrigs := func() map[string]string {
		rows, err := scopedPool.Query(ctx, `
			SELECT t.tgname, pg_get_triggerdef(t.oid, true)
			FROM pg_trigger t
			JOIN pg_class c ON c.oid = t.tgrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND NOT t.tgisinternal
			ORDER BY t.tgname`, schema)
		if err != nil {
			t.Fatalf("snapshot triggers: %v", err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var name, ddl string
			if err := rows.Scan(&name, &ddl); err != nil {
				t.Fatal(err)
			}
			out[name] = ddl
		}
		return out
	}
	beforeTrigs := snapshotTrigs()
	if len(beforeTrigs) != 2 {
		t.Fatalf("expected 2 triggers before migration, got %d", len(beforeTrigs))
	}

	sqPath := filepath.Join(t.TempDir(), "e7_identity.sqlite")
	sqDB, err := sqlitedb.Open(ctx, sqPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sqDB.Close() }()
	if _, err := sqDB.ExecContext(ctx, `
		CREATE TABLE mailboxes (
			id INTEGER PRIMARY KEY,
			tenant_id INTEGER NOT NULL,
			address TEXT NOT NULL,
			status TEXT NOT NULL,
			password_hash TEXT NOT NULL,
			password_version INTEGER NOT NULL,
			password_revoke_pending INTEGER NOT NULL
		);
		INSERT INTO mailboxes(id, tenant_id, address, status, password_hash, password_version, password_revoke_pending)
		VALUES(1, 1, 'user-a@example.test', 'active', 'hash1', 2, 1);

		CREATE TABLE mailbox_scheduled_sends (
			id INTEGER PRIMARY KEY,
			mailbox_id INTEGER NOT NULL,
			status TEXT NOT NULL
		);
		INSERT INTO mailbox_scheduled_sends(id, mailbox_id, status) VALUES(101, 1, 'queued'), (205, 1, 'sent');

		CREATE TABLE mailbox_aliases (
			id INTEGER PRIMARY KEY,
			tenant_id INTEGER NOT NULL,
			address TEXT NOT NULL,
			sender_logins TEXT NOT NULL
		);
		INSERT INTO mailbox_aliases(id, tenant_id, address, sender_logins) VALUES(14, 1, 'alias@example.test', '["user-a@example.test"]');
	`); err != nil {
		t.Fatal(err)
	}

	tm := datamigrate.TypeMap{
		"mailboxes": {
			"id":                      {Table: "mailboxes", Column: "id", PGType: "bigint", SQLiteType: "INTEGER", Encoding: datamigrate.EncodingPassthrough},
			"tenant_id":               {Table: "mailboxes", Column: "tenant_id", PGType: "bigint", SQLiteType: "INTEGER", Encoding: datamigrate.EncodingPassthrough},
			"address":                 {Table: "mailboxes", Column: "address", PGType: "text", SQLiteType: "TEXT", Encoding: datamigrate.EncodingPassthrough},
			"status":                  {Table: "mailboxes", Column: "status", PGType: "text", SQLiteType: "TEXT", Encoding: datamigrate.EncodingPassthrough},
			"password_hash":           {Table: "mailboxes", Column: "password_hash", PGType: "text", SQLiteType: "TEXT", Encoding: datamigrate.EncodingPassthrough},
			"password_version":        {Table: "mailboxes", Column: "password_version", PGType: "bigint", SQLiteType: "INTEGER", Encoding: datamigrate.EncodingPassthrough},
			"password_revoke_pending": {Table: "mailboxes", Column: "password_revoke_pending", PGType: "boolean", SQLiteType: "INTEGER", Encoding: datamigrate.EncodingPassthrough},
		},
		"mailbox_scheduled_sends": {
			"id":         {Table: "mailbox_scheduled_sends", Column: "id", PGType: "bigint", SQLiteType: "INTEGER", Encoding: datamigrate.EncodingPassthrough},
			"mailbox_id": {Table: "mailbox_scheduled_sends", Column: "mailbox_id", PGType: "bigint", SQLiteType: "INTEGER", Encoding: datamigrate.EncodingPassthrough},
			"status":     {Table: "mailbox_scheduled_sends", Column: "status", PGType: "text", SQLiteType: "TEXT", Encoding: datamigrate.EncodingPassthrough},
		},
		"mailbox_aliases": {
			"id":            {Table: "mailbox_aliases", Column: "id", PGType: "bigint", SQLiteType: "INTEGER", Encoding: datamigrate.EncodingPassthrough},
			"tenant_id":     {Table: "mailbox_aliases", Column: "tenant_id", PGType: "bigint", SQLiteType: "INTEGER", Encoding: datamigrate.EncodingPassthrough},
			"address":       {Table: "mailbox_aliases", Column: "address", PGType: "text", SQLiteType: "TEXT", Encoding: datamigrate.EncodingPassthrough},
			"sender_logins": {Table: "mailbox_aliases", Column: "sender_logins", PGType: "text[]", SQLiteType: "TEXT", Encoding: datamigrate.EncodingJSONArray},
		},
	}

	rep, err := MigrateDBSQLiteToPG(ctx, sqDB, scopedPool, tm, nil, false)
	if err != nil {
		t.Fatalf("MigrateDBSQLiteToPG failed: %v", err)
	}
	if !rep.ZeroDiff || rep.RowsUpserted != 4 || rep.SequencesReset != 3 {
		t.Fatalf("unexpected report: %+v", rep)
	}

	// 2. Verify identity sequence was advanced past 205.
	var nextID int64
	if err := scopedPool.QueryRow(ctx, `INSERT INTO mailbox_scheduled_sends (mailbox_id, status) VALUES (1, 'queued') RETURNING id`).Scan(&nextID); err != nil {
		t.Fatalf("insert next scheduled send: %v", err)
	}
	if nextID <= 205 {
		t.Fatalf("expected identity sequence > 205 after setval, got %d", nextID)
	}

	// 3. Verify exact byte-for-byte trigger parity after restore.
	afterTrigs := snapshotTrigs()
	if len(afterTrigs) != len(beforeTrigs) {
		t.Fatalf("trigger count mismatch: got %d, want %d", len(afterTrigs), len(beforeTrigs))
	}
	for k, wantDDL := range beforeTrigs {
		if afterTrigs[k] != wantDDL {
			t.Fatalf("trigger %s mismatch:\ngot:  %s\nwant: %s", k, afterTrigs[k], wantDDL)
		}
	}

	// 4. Verify restored triggers are live and enforce invariants post-migration.
	if _, err := scopedPool.Exec(ctx, `UPDATE mailboxes SET password_version = 3 WHERE id = 1`); err == nil || !strings.Contains(err.Error(), "42501") {
		t.Fatalf("expected restored mailbox_password_version_guard (42501), got %v", err)
	}
	if _, err := scopedPool.Exec(ctx, `UPDATE mailbox_aliases SET sender_logins = ARRAY['ghost@example.test']::text[] WHERE id = 14`); err == nil || !strings.Contains(err.Error(), "23514") {
		t.Fatalf("expected restored alias_sender_logins_guard (23514), got %v", err)
	}
}

package miadbinventory

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureDDL = `CREATE TABLE users(id INTEGER PRIMARY KEY AUTOINCREMENT,email TEXT NOT NULL UNIQUE,password TEXT NOT NULL,extra,privileges TEXT NOT NULL DEFAULT '',quota TEXT NOT NULL DEFAULT '0');
CREATE TABLE aliases(id INTEGER PRIMARY KEY AUTOINCREMENT,source TEXT NOT NULL UNIQUE,destination TEXT NOT NULL,permitted_senders TEXT);
CREATE TABLE auto_aliases(id INTEGER PRIMARY KEY AUTOINCREMENT,source TEXT NOT NULL UNIQUE,destination TEXT NOT NULL,permitted_senders TEXT);
CREATE TABLE mfa(id INTEGER PRIMARY KEY AUTOINCREMENT,user_id INTEGER NOT NULL,type TEXT NOT NULL,secret TEXT NOT NULL,mru_token TEXT,label TEXT,FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE);`

func fixture(t *testing.T, extra string) Options {
	t.Helper()
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip("fixed Python SQLite runtime unavailable")
	}
	dir := t.TempDir()
	options := Options{ConfigPath: filepath.Join(dir, "mailinabox.conf"), SQLitePath: filepath.Join(dir, "users.sqlite"), MappingPath: filepath.Join(dir, "mapping.json")}
	if err := os.WriteFile(options.ConfigPath, []byte("STORAGE_ROOT=/home/user-data\nPRIMARY_HOSTNAME=box.fixture.test\nSECRET_KEY=private-config-never-output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mapping := `{"version":1,"domains":[{"domain":"one.fixture.test","tenant_ref":"customer-migration"}]}`
	if err := os.WriteFile(options.MappingPath, []byte(mapping), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `import sqlite3,sys
c=sqlite3.connect(sys.argv[1]);c.executescript(sys.argv[2]);c.execute("INSERT INTO users(email,password)VALUES(?,?)",('owner@one.fixture.test','{SHA512-CRYPT}$6$fixturesalt$'+'A'*86));c.executescript(sys.argv[3]);c.commit();c.close()`
	if err := exec.CommandContext(context.Background(), "/usr/bin/python3", "-I", "-c", script, options.SQLitePath, fixtureDDL, extra).Run(); err != nil {
		t.Fatal("create private SQLite fixture failed")
	}
	return options
}

func hasIssue(report Report, code string) bool {
	for _, issue := range report.Blockers {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func TestSnapshotAndExplicitMappingAreReadOnlyAndRedacted(t *testing.T) {
	options := fixture(t, "")
	before, err := os.ReadFile(options.SQLitePath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(options.SQLitePath)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Inspect(context.Background(), options)
	if err != nil || report.Status != "dry_run_complete" || report.Counts["users"] != 1 || report.UnlimitedQuotaMailboxes != 1 || report.Changed || report.ApplyAvailable || report.CutoverReady {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	raw, _ := json.Marshal(report)
	for _, secret := range []string{"owner@", "one.fixture.test", "fixturesalt", "SHA512", "private-config", "customer-migration"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("report exposed private source value")
		}
	}
	after, err := os.ReadFile(options.SQLitePath)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(options.SQLitePath)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("dry-run changed source database")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(options.SQLitePath + suffix); !os.IsNotExist(err) {
			t.Fatal("dry-run created source sidecar")
		}
	}
	options.MappingPath = ""
	missing, err := Inspect(context.Background(), options)
	if err != nil || !hasIssue(missing, "MISSING_EXPLICIT_TENANT_MAPPING") {
		t.Fatalf("missing mapping accepted: %v %+v", err, missing)
	}
}

func TestUnsafeSourceConditionsBlockWithoutEchoingPrivateValues(t *testing.T) {
	tests := []struct{ name, sql, code string }{
		{"unknown-column", "ALTER TABLE users ADD COLUMN custom_secret TEXT;", "UNKNOWN_SOURCE_SCHEMA"},
		{"unknown-table", "CREATE TABLE foreign_secret(value TEXT);", "UNKNOWN_SOURCE_SCHEMA"},
		{"duplicate-case", "INSERT INTO users(email,password) SELECT 'OWNER@one.fixture.test',password FROM users;", "DUPLICATE_MAILBOX_ADDRESS"},
		{"unsupported-hash", "UPDATE users SET password='{ARGON2}private-hash-never-output';", "UNSUPPORTED_PASSWORD_HASH"},
		{"cross-domain-alias", "INSERT INTO aliases(source,destination)VALUES('sales@one.fixture.test','outside@other.fixture.test');", "CROSS_DOMAIN_ALIAS_REVIEW_REQUIRED"},
		{"sender-restrictions", "INSERT INTO aliases(source,destination,permitted_senders)VALUES('sales@one.fixture.test','owner@one.fixture.test','owner@one.fixture.test');", "SENDER_RESTRICTIONS_REVIEW_REQUIRED"},
		{"orphan-mfa", "INSERT INTO mfa(user_id,type,secret)VALUES(99,'totp','private-mfa-never-output');", "ORPHAN_MFA_REFERENCE"},
		{"alias-cycle", "INSERT INTO aliases(source,destination)VALUES('first@one.fixture.test','second@one.fixture.test'),('second@one.fixture.test','first@one.fixture.test');", "ALIAS_CYCLE_OR_DEPTH_REVIEW_REQUIRED"},
		{"unresolved-local-alias", "INSERT INTO aliases(source,destination)VALUES('sales@one.fixture.test','missing@one.fixture.test');", "UNRESOLVED_LOCAL_ALIAS_TARGET"},
		{"unmapped-extra-metadata", "UPDATE users SET extra='private-extra-never-output';", "EXTRA_METADATA_REVIEW_REQUIRED"},
		{"quota-units", "UPDATE users SET quota='10G';", "UNSUPPORTED_QUOTA"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := fixture(t, test.sql)
			report, err := Inspect(context.Background(), options)
			if err != nil || !hasIssue(report, test.code) {
				t.Fatalf("expected %s report=%+v err=%v", test.code, report, err)
			}
			raw, _ := json.Marshal(report)
			if strings.Contains(string(raw), "private-") {
				t.Fatal("source secrets leaked")
			}
		})
	}
}

func TestConfigIsDataAndCannotExecuteShell(t *testing.T) {
	options := fixture(t, "")
	marker := filepath.Join(filepath.Dir(options.ConfigPath), "shell-executed")
	raw := "STORAGE_ROOT=$(touch " + marker + ")\n"
	if err := os.WriteFile(options.ConfigPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(context.Background(), options); err == nil {
		t.Fatal("dynamic source root accepted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("source config executed a shell")
	}
}

func TestRealSourceShapeSixDomainsFortyThreeUnlimitedMailboxes(t *testing.T) {
	options := fixture(t, "")
	script := `import sqlite3,sys,json
c=sqlite3.connect(sys.argv[1]);c.execute('DELETE FROM users')
for i in range(43):c.execute('INSERT INTO users(email,password)VALUES(?,?)',('member%d@d%d.fixture.test'%(i,i%6),'{SHA512-CRYPT}$6$fixturesalt$'+'A'*86))
for table,count in [('aliases',2),('auto_aliases',22)]:
 for i in range(count):c.execute('INSERT INTO '+table+'(source,destination)VALUES(?,?)',(table.replace('_','-')+'%d@d%d.fixture.test'%(i,i%6),'member%d@d%d.fixture.test'%(i%43,i%6)))
c.commit();c.close()
json.dump({'version':1,'domains':[{'domain':'d%d.fixture.test'%i,'tenant_ref':'same-tenant'}for i in range(6)]},open(sys.argv[2],'w'))`
	if err := exec.CommandContext(context.Background(), "/usr/bin/python3", "-I", "-c", script, options.SQLitePath, options.MappingPath).Run(); err != nil {
		t.Fatal("create source-shape fixture failed")
	}
	// Manual and automatic alias identities may coincide in a source; this is a
	// review blocker rather than silently choosing precedence or merging rows.
	report, err := Inspect(context.Background(), options)
	if err != nil || report.Counts["users"] != 43 || report.Counts["aliases"] != 2 || report.Counts["auto_aliases"] != 22 || report.Counts["mfa"] != 0 || len(report.Domains) != 6 || report.UnlimitedQuotaMailboxes != 43 || report.Status != "dry_run_complete" || report.TargetTenantGroups != 1 {
		t.Fatalf("shape mismatch report=%+v err=%v", report, err)
	}
	for _, domain := range report.Domains {
		if domain.MappingRef == "" || domain.TargetGroupRef != "tenant:1" {
			t.Fatal("single-tenant mapping was not accepted")
		}
	}
	options.MappingPath = ""
	report, err = Inspect(context.Background(), options)
	if err != nil || !hasIssue(report, "MISSING_EXPLICIT_TENANT_MAPPING") {
		t.Fatal("unknown real domain ownership was inferred")
	}
}

func TestWALOrJournalLayoutBlocksWithoutTouchingSidecars(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(suffix, func(t *testing.T) {
			options := fixture(t, "")
			path := options.SQLitePath + suffix
			data := []byte("private-uncommitted-or-WAL-facts")
			if err := os.WriteFile(path, data, 0o400); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			report, err := Inspect(context.Background(), options)
			if err != nil || !hasIssue(report, "SQLITE_SIDECAR_REQUIRES_CONSISTENT_READONLY_SNAPSHOT") {
				t.Fatalf("sidecar was ignored: %+v %v", report, err)
			}
			afterData, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(afterData) != string(data) || !after.ModTime().Equal(before.ModTime()) {
				t.Fatal("source sidecar was changed")
			}
		})
	}
}

func TestMappingCannotSilentlySplitTheConfirmedSingleTenant(t *testing.T) {
	options := fixture(t, "INSERT INTO users(email,password) SELECT 'second@two.fixture.test',password FROM users;")
	raw := `{"version":1,"domains":[{"domain":"one.fixture.test","tenant_ref":"first-tenant"},{"domain":"two.fixture.test","tenant_ref":"second-tenant"}]}`
	if err := os.WriteFile(options.MappingPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Inspect(context.Background(), options)
	if err != nil || report.TargetTenantGroups != 2 || !hasIssue(report, "MULTIPLE_TARGET_TENANTS_REQUIRE_REVIEW") {
		t.Fatalf("split mapping did not require review: %+v %v", report, err)
	}
}

func TestMetadataSizeAndSchemaObjectsAreBoundedBeforeBulkRead(t *testing.T) {
	t.Run("file-size", func(t *testing.T) {
		options := fixture(t, "")
		// A sparse fixture proves preflight rejects the size before SQLite opens it.
		if err := os.Truncate(options.SQLitePath, (512<<20)+1); err != nil {
			t.Fatal(err)
		}
		report, err := Inspect(context.Background(), options)
		if err != nil || !hasIssue(report, "SOURCE_METADATA_FILE_LIMIT_EXCEEDED") {
			t.Fatalf("oversized source accepted: %+v %v", report, err)
		}
	})
	t.Run("schema-objects", func(t *testing.T) {
		var extra strings.Builder
		for i := 0; i < 129; i++ {
			_, _ = fmt.Fprintf(&extra, "CREATE TABLE extra_%d(value TEXT);", i)
		}
		options := fixture(t, extra.String())
		report, err := Inspect(context.Background(), options)
		if err != nil || !hasIssue(report, "SOURCE_SCHEMA_OBJECT_LIMIT_EXCEEDED") {
			t.Fatalf("object limit was not enforced: %+v %v", report, err)
		}
	})
}

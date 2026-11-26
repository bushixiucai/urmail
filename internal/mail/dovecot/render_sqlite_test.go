package dovecot_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/dovecot"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/migrations_sqlite"
)

// sqliteAuthNode renders the single-engine auth surface against the SQLite
// snapshot.
func sqliteAuthNode(t *testing.T, snapshotPath string) dovecot.NodeConfig {
	t.Helper()
	return dovecot.NodeConfig{
		Hostname:      "box.example.test",
		PrimaryDomain: "example.test",
		StorageRoot:   "/srv/urmail/data/mail",
		TLSCertFile:   "/certs/mail.cert",
		TLSKeyFile:    "/certs/mail.key",
		AuthEngine:    "sqlite",
		AuthDBPath:    snapshotPath,
	}
}

// renderedSQLiteQueries extracts the three auth SQL statements dovecot will
// run, with the dovecot placeholders expanded the way dovecot expands them
// for an IMAP login of u@example.test.
func renderedSQLiteQueries(t *testing.T, conf string) (password, user, iterate string) {
	t.Helper()
	for _, line := range strings.Split(conf, "\n") {
		name, value, found := strings.Cut(line, " = ")
		if !found {
			continue
		}
		sql := strings.TrimSuffix(strings.TrimSpace(value), ";")
		sql = strings.ReplaceAll(sql, "%u", "u@example.test")
		sql = strings.ReplaceAll(sql, "%d", "example.test")
		sql = strings.ReplaceAll(sql, "%n", "u")
		sql = strings.ReplaceAll(sql, "%s", "imap")
		switch name {
		case "password_query":
			password = sql
		case "user_query":
			user = sql
		case "iterate_query":
			iterate = sql
		}
	}
	if password == "" || user == "" || iterate == "" {
		t.Fatalf("rendered auth config missing queries:\n%s", conf)
	}
	return password, user, iterate
}

// TestSQLiteAuthQueriesMatchSchema is the slice-1 drift regression: the
// rendered auth SQL must prepare and run against the real migrations_sqlite
// schema. The earlier draft referenced tenants.tenant_id, domains.domain_id
// and domains.mail_ready — none of which exist in SQLite — so dovecot's auth
// SQL failed on load and the whole 993/587 authentication face went down.
func TestSQLiteAuthQueriesMatchSchema(t *testing.T) {
	ctx := context.Background()
	files, err := dovecot.Generate(sqliteAuthNode(t, "/var/lib/urmail/dovecot-auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	conf, ok := files[dovecot.FileDBConf]
	if !ok {
		t.Fatalf("auth sql config not rendered: %v", files)
	}
	for _, banned := range []string{"t.tenant_id", "d.domain_id", "d.mail_ready"} {
		if strings.Contains(conf, banned) {
			t.Fatalf("auth config references non-SQLite column %q:\n%s", banned, conf)
		}
	}
	if !strings.Contains(conf, "provisioning_state = 'ready'") || !strings.Contains(conf, "d.verified_at IS NOT NULL") {
		t.Fatalf("auth config lost the domain mail-ready predicate:\n%s", conf)
	}

	password, user, iterate := renderedSQLiteQueries(t, conf)
	raw, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "authschema.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if err = sqlitedb.Migrate(ctx, raw, migrations_sqlite.FS, "."); err != nil {
		t.Fatal(err)
	}
	seeds := []string{
		`INSERT INTO brands(slug,name) VALUES('default','Default')`,
		`INSERT INTO tenants(brand_id,name,slug,status) VALUES(1,'T','t','active')`,
		`INSERT INTO users(email,password_hash) VALUES('admin@example.test','h')`,
		`INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at,mode) VALUES(1,'example.test','active','tok',1,'delegated')`,
		`UPDATE domains SET provisioning_state='ready' WHERE id=1`,
		`INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status,quota_bytes) VALUES(1,1,'u','u@example.test','$5$hash','active',1048576)`,
	}
	for _, s := range seeds {
		if _, err = raw.ExecContext(ctx, s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}

	// password_query returns exactly the ready-domain mailbox.
	var addr, hash string
	if err = raw.QueryRowContext(ctx, password).Scan(&addr, &hash); err != nil {
		t.Fatalf("password_query failed on real schema: %v\n%s", err, password)
	}
	if addr != "u@example.test" || hash != "$5$hash" {
		t.Fatalf("password_query got %q/%q", addr, hash)
	}

	// user_query carries the quota, acl and sieve fields dovecot needs
	// (user/uid/gid/home/quota_rule/acl_groups + the five legacy sieve fields,
	// four of which are NULL for managed mailboxes).
	var uqAddr, uid, gid, home, quotaRule, acl, sieve string
	var sieveDir, sieveBefore2, sieveAfter, sieveRedirect sql.NullString
	if err = raw.QueryRowContext(ctx, user).Scan(&uqAddr, &uid, &gid, &home, &quotaRule, &acl, &sieve, &sieveDir, &sieveBefore2, &sieveAfter, &sieveRedirect); err != nil {
		t.Fatalf("user_query failed on real schema: %v\n%s", err, user)
	}
	if uqAddr != "u@example.test" || uid != "mail" || gid != "mail" {
		t.Fatalf("user_query identity %q/%q/%q", uqAddr, uid, gid)
	}
	if quotaRule != "*:bytes=1048576" || acl != "" {
		t.Fatalf("user_query quota/acl %q/%q", quotaRule, acl)
	}
	if !strings.Contains(home, "/srv/urmail/data/mail/mail/mailboxes/example.test/u") || !strings.Contains(sieve, "sieve") {
		t.Fatalf("user_query home/sieve %q/%q", home, sieve)
	}

	// iterate_query lists active mailboxes on ready domains.
	var listed string
	if err = raw.QueryRowContext(ctx, iterate).Scan(&listed); err != nil {
		t.Fatalf("iterate_query failed on real schema: %v\n%s", err, iterate)
	}
	if listed != "u@example.test" {
		t.Fatalf("iterate_query listed %q", listed)
	}

	// Not-ready domain (provisioning pending) must hide the mailbox again:
	// the inlined predicate keeps the pre-S6 gate semantics.
	if _, err = raw.ExecContext(ctx, `UPDATE domains SET provisioning_state='pending' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err = raw.QueryRowContext(ctx, password).Scan(&addr, &hash); err == nil {
		t.Fatal("password_query accepted a mailbox on a not-ready domain")
	}
}

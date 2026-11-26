package reconcile_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/reconcile"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/migrations_sqlite"
)

// TestAssembleConfigContextOnSQLite proves the platform queries used by the
// renderer run on the single engine, including the alias sender-login array
// that PostgreSQL renders with ARRAY(SELECT …).
func TestAssembleConfigContextOnSQLite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "urmail.sqlite")
	raw, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = sqlitedb.Migrate(ctx, raw, migrations_sqlite.FS, "."); err != nil {
		t.Fatal(err)
	}
	pool, err := db.NewSQLitePool(raw, isolation.ScopePlatform)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	cfg := &config.Config{MyHostname: "box.example.test", PrimaryDomain: "example.test", StorageRoot: "/var/mail", DatabaseURLDovecot: "host=127.0.0.1 dbname=urmail"}
	assembled, err := reconcile.AssembleConfigContext(ctx, cfg, &reconcile.VersionedStore{StoreQuerier: db.Queries(pool), DB: pool}, "192.0.2.1", nil)
	if err != nil {
		t.Fatalf("assemble on sqlite: %v", err)
	}
	if assembled.DovecotNode.TLSCertFile != "/var/lib/urmail-agent/certs/mail/box.example.test.cert" {
		t.Fatalf("default TLSCertFile=%q", assembled.DovecotNode.TLSCertFile)
	}

	cfg.AgentStateRoot = "/srv/urmail/data/agent-state"
	assembledCustom, err := reconcile.AssembleConfigContext(ctx, cfg, &reconcile.VersionedStore{StoreQuerier: db.Queries(pool), DB: pool}, "192.0.2.1", nil)
	if err != nil {
		t.Fatalf("assemble on sqlite with AgentStateRoot: %v", err)
	}
	if assembledCustom.DovecotNode.TLSCertFile != "/srv/urmail/data/agent-state/certs/mail/box.example.test.cert" ||
		assembledCustom.DovecotNode.TLSKeyFile != "/srv/urmail/data/agent-state/certs/mail/box.example.test.key" {
		t.Fatalf("custom TLS paths=%q %q", assembledCustom.DovecotNode.TLSCertFile, assembledCustom.DovecotNode.TLSKeyFile)
	}
}

// TestAssembleConfigContextReadsSQLitePolicyMX covers the four cell shapes
// S7 hit on a production-imported library: empty, one host, two hosts, and a
// leftover PostgreSQL text-array literal. The renderer must scan all four.
func TestAssembleConfigContextReadsSQLitePolicyMX(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "urmail.sqlite")
	raw, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = sqlitedb.Migrate(ctx, raw, migrations_sqlite.FS, "."); err != nil {
		t.Fatal(err)
	}
	pool, err := db.NewSQLitePool(raw, isolation.ScopePlatform)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	const host = "box.example.test"
	if _, err = raw.ExecContext(ctx, `INSERT INTO brands(slug,name) VALUES('default','Default')`); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(ctx, `INSERT INTO tenants(brand_id,name,slug,status) VALUES(1,'Tenant','tenant','active')`); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		fqdn string
		mode string
		mx   string
		want []string
	}{
		{"empty.example.test", "none", "[]", nil},
		{"one.example.test", "testing", `["box.example.test"]`, []string{host}},
		{"two.example.test", "testing", `["mx1.example.test","box.example.test"]`, []string{"mx1.example.test", host}},
		{"pg.example.test", "testing", "{box.example.test,mx2.example.test}", []string{host, "mx2.example.test"}},
	}
	for i, tc := range cases {
		if _, err = raw.ExecContext(ctx, `INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at,mode) VALUES(1,?, 'active', ?, 1, 'managed')`, tc.fqdn, "tok"+tc.fqdn); err != nil {
			t.Fatal(err)
		}
		if _, err = raw.ExecContext(ctx, `INSERT INTO domain_transport_security(tenant_id,domain_id,mtasts_policy_id,mtasts_mode,tls_rpt_rua,enabled,policy_mx,max_age,published_at,cache_safe_after) VALUES(1, ?, 'p1', ?, 'mailto:r@example.test', 1, ?, 86400, 1, 1)`, int64(i+1), tc.mode, tc.mx); err != nil {
			t.Fatalf("insert %s: %v", tc.fqdn, err)
		}
	}
	cfg := &config.Config{MyHostname: host, PrimaryDomain: "example.test", StorageRoot: "/var/mail", DatabaseURLDovecot: "host=127.0.0.1 dbname=urmail"}
	if _, err = reconcile.AssembleConfigContext(ctx, cfg, &reconcile.VersionedStore{StoreQuerier: db.Queries(pool), DB: pool}, "192.0.2.1", nil); err != nil {
		t.Fatalf("assemble with policy_mx rows: %v", err)
	}
	scoped := isolation.WithScope(ctx, isolation.ScopeContext{Scope: isolation.ScopePlatform})
	rows, err := db.Queries(pool).PlatformListTransportSecurity(scoped)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(cases) {
		t.Fatalf("rows=%d", len(rows))
	}
	for i, tc := range cases {
		got := rows[i].PolicyMx
		if len(got) == 0 && len(tc.want) == 0 {
			continue
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%s got %#v want %#v", tc.fqdn, got, tc.want)
		}
		for j := range tc.want {
			if got[j] != tc.want[j] {
				t.Fatalf("%s got %#v want %#v", tc.fqdn, got, tc.want)
			}
		}
	}
}

func TestAssembleConfigContextRejectsMalformedPolicyMX(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "urmail.sqlite")
	raw, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = sqlitedb.Migrate(ctx, raw, migrations_sqlite.FS, "."); err != nil {
		t.Fatal(err)
	}
	pool, err := db.NewSQLitePool(raw, isolation.ScopePlatform)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err = raw.ExecContext(ctx, `INSERT INTO brands(slug,name) VALUES('default','Default')`); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(ctx, `INSERT INTO tenants(brand_id,name,slug,status) VALUES(1,'Tenant','tenant','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(ctx, `INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at,mode) VALUES(1,'bad.example.test','active','tok',1,'managed')`); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(ctx, `INSERT INTO domain_transport_security(tenant_id,domain_id,mtasts_policy_id,mtasts_mode,tls_rpt_rua,enabled,policy_mx,max_age,published_at,testing_since,cache_safe_after,observed_at) VALUES(1,1,'p1','testing','mailto:r@example.test',1,'not-an-array',86400,1,1,1,1)`); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{MyHostname: "box.example.test", PrimaryDomain: "example.test", StorageRoot: "/var/mail", DatabaseURLDovecot: "host=127.0.0.1 dbname=urmail"}
	_, err = reconcile.AssembleConfigContext(ctx, cfg, &reconcile.VersionedStore{StoreQuerier: db.Queries(pool), DB: pool}, "192.0.2.1", nil)
	if err == nil || !strings.Contains(err.Error(), "sqlite text array") {
		t.Fatalf("malformed policy_mx err=%v", err)
	}
}

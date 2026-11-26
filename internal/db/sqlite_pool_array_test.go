package db_test

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/migrations_sqlite"
)

// TestSQLitePoolAllSchemaArrayColumns covers the six array columns the
// SQLite pool scans: five text[] and one bigint[]. Cells use the JSON form
// the importer writes, including decimal strings for bigint[].
func TestSQLitePoolAllSchemaArrayColumns(t *testing.T) {
	ctx := context.Background()
	raw, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "arrays.sqlite"))
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

	digest := strings.Repeat("a", 64)
	seeds := []string{
		`INSERT INTO brands(slug,name) VALUES('default','Default')`,
		`INSERT INTO tenants(brand_id,name,slug,status) VALUES(1,'T','t','active')`,
		`INSERT INTO settings(scope,scope_id,key,value) VALUES('platform',0,'mailbox_limit','-1')`,
		`INSERT INTO users(email,password_hash) VALUES('u@example.test','h')`,
		`INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at,mode) VALUES(1,'example.test','active','tok',1,'delegated')`,
		`UPDATE domains SET provisioning_state='ready' WHERE id=1`,
		`INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES(1,1,'u','u@example.test','h','active')`,
		`INSERT INTO ai_provider_configs(tenant_id,mode,provider,endpoint,model,approved_endpoints) VALUES(1,'byo','openai','https://api.openai.com','m','["https://api.openai.com","https://proxy.example.test"]')`,
		`INSERT INTO api_tokens(tenant_id,name,prefix,hash,scopes) VALUES(1,'tok','urm_','hash1','["mail:read","mail:send"]')`,
		`INSERT INTO application_credentials(user_id,name,token_hash,scopes,expires_at) VALUES(1,'app','hashapp','["imap","smtp"]',2000000000000000)`,
		`INSERT INTO domain_transport_security(tenant_id,domain_id,mtasts_policy_id,mtasts_mode,tls_rpt_rua,enabled,policy_mx,max_age,published_at,cache_safe_after) VALUES(1,1,'p1','testing','mailto:r@example.test',1,'["mx1.example.test","mx2.example.test"]',86400,1,1)`,
		`INSERT INTO mailbox_aliases(tenant_id,domain_id,local_part,target,sender_logins) VALUES(1,1,'a','u@example.test','["u@example.test"]')`,
		`INSERT INTO mailbox_import_jobs(tenant_id,mailbox_id,request_key,format,filename,folder,uid_validity,source_digest,preview_digest,duplicate_policy,source_bytes,status) VALUES(1,1,'request01','mbox','a.mbox','INBOX',1,'` + digest + `','` + digest + `','skip',1,'preview')`,
		`INSERT INTO mailbox_import_items(tenant_id,mailbox_id,job_id,item_index,status,baseline_uids) VALUES(1,1,1,1,'imported','["101","202","303"]')`,
	}
	for _, stmt := range seeds {
		if _, err = raw.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	scoped := isolation.WithScope(ctx, isolation.ScopeContext{Scope: isolation.ScopePlatform})
	checkStrings := []struct {
		sql  string
		want []string
	}{
		{`SELECT approved_endpoints FROM ai_provider_configs WHERE tenant_id=1`, []string{"https://api.openai.com", "https://proxy.example.test"}},
		{`SELECT scopes FROM api_tokens WHERE id=1`, []string{"mail:read", "mail:send"}},
		{`SELECT scopes FROM application_credentials WHERE id=1`, []string{"imap", "smtp"}},
		{`SELECT policy_mx FROM domain_transport_security WHERE id=1`, []string{"mx1.example.test", "mx2.example.test"}},
		{`SELECT sender_logins FROM mailbox_aliases WHERE id=1`, []string{"u@example.test"}},
	}
	for _, tc := range checkStrings {
		var got []string
		if err = pool.QueryRow(scoped, tc.sql).Scan(&got); err != nil {
			t.Fatalf("%s: %v", tc.sql, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s got %#v want %#v", tc.sql, got, tc.want)
		}
	}
	var uids []int64
	if err = pool.QueryRow(scoped, `SELECT baseline_uids FROM mailbox_import_items WHERE item_index=1`).Scan(&uids); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(uids, []int64{101, 202, 303}) {
		t.Fatalf("baseline_uids=%#v", uids)
	}
	if _, err = pool.Exec(scoped, `UPDATE mailbox_import_items SET baseline_uids=$1 WHERE item_index=1`, []int64{7, 8}); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(scoped, `SELECT baseline_uids FROM mailbox_import_items WHERE item_index=1`).Scan(&uids); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(uids, []int64{7, 8}) {
		t.Fatalf("updated=%#v", uids)
	}
	if _, err = raw.ExecContext(ctx, `UPDATE domain_transport_security SET policy_mx='not-an-array' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	var bad []string
	if err = pool.QueryRow(scoped, `SELECT policy_mx FROM domain_transport_security WHERE id=1`).Scan(&bad); err == nil || !strings.Contains(err.Error(), "sqlite text array") {
		t.Fatalf("malformed text err=%v", err)
	}
	if _, err = raw.ExecContext(ctx, `UPDATE mailbox_import_items SET baseline_uids='["bad"]' WHERE item_index=1`); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(scoped, `SELECT baseline_uids FROM mailbox_import_items WHERE item_index=1`).Scan(&uids); err == nil || !strings.Contains(err.Error(), "sqlite bigint array") {
		t.Fatalf("malformed bigint err=%v", err)
	}
}

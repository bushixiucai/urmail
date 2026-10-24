package handlers

import (
	"database/sql"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestRetentionUpgradeStopsLegacyPolicyWithoutInventingGrant(t *testing.T) {
	ctx := t.Context()
	dsn := testdb.New(t)
	source, err := iofs.New(db.FS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := migratepgx.WithInstance(connection, &migratepgx.Config{})
	if err != nil {
		t.Fatal(err)
	}
	migration, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		t.Fatal(err)
	}
	if err = migration.Migrate(57); err != nil {
		t.Fatal(err)
	}
	if a, b := migration.Close(); a != nil || b != nil {
		t.Fatal(a, b)
	}
	p, err := db.OpenPlatform(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var brand, tenant, user int64
	if err = p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('legacy-retention','Legacy') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Legacy','legacy-retention','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('legacy-retention@example.test','x') RETURNING id").Scan(&user); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, "INSERT INTO user_tenant_roles VALUES($1,$2,'owner')", user, tenant); err != nil {
		t.Fatal(err)
	}
	tx, err := p.UserTx(ctx, tenant, user)
	if err != nil {
		t.Fatal(err)
	}
	q := store.New(tx)
	if _, err = q.CreateComplianceGrant(ctx, store.CreateComplianceGrantParams{TenantID: tenant, UserID: user, Scope: "full_content", Reason: "Legacy read authority only", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}, GrantedBy: pgtype.Int8{Int64: user, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "WITH p AS (INSERT INTO retention_previews(tenant_id,retention_days,candidate_count,candidate_digest,created_by,confirmed_at) VALUES($1,30,0,'old-preview',$2,now()) RETURNING id) INSERT INTO retention_policies(tenant_id,retention_days,action,active,created_by,confirmed_preview) SELECT $1,30,'soft_delete',true,$2,id FROM p", tenant, user); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	upgraded, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = upgraded.Close() }()
	if err = upgraded.Up(ctx); err != nil {
		t.Fatal(err)
	}
	var active bool
	var grants, audits int
	if err = p.QueryRow(ctx, "SELECT active FROM retention_policies WHERE tenant_id=$1", tenant).Scan(&active); err != nil || active {
		t.Fatalf("legacy policy remained armed: %v %v", active, err)
	}
	if err = p.QueryRow(ctx, "SELECT count(*) FROM compliance_grants WHERE tenant_id=$1 AND scope='retention_manage'", tenant).Scan(&grants); err != nil || grants != 0 {
		t.Fatal("migration invented owner authorization", err)
	}
	if err = p.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action='MIGRATE retention.authorization'", tenant).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("legacy policy disable audit missing", err)
	}
}

package archive

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// ARC-002: a legal hold must stop both the retention soft-delete transition
// and physical purge candidates, while an unheld copy of the same age is
// expired and purged by the same worker pass.
func TestLegalHoldBlocksRetentionExpiryTransition(t *testing.T) {
	pool, tenant, user, mailbox := retentionDatabase(t)
	ctx := context.Background()
	w := &Worker{Pool: pool, Spool: filepath.Join(t.TempDir(), "spool"), Archive: filepath.Join(t.TempDir(), "archive")}
	heldID := retentionCopy(t, w, tenant, mailbox, "held-copy")
	freeID := retentionCopy(t, w, tenant, mailbox, "free-copy")
	if _, err := pool.Exec(ctx, "UPDATE archive_entries SET legal_hold=true WHERE id=$1", heldID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{heldID, freeID} {
		if _, err := pool.Exec(ctx, "UPDATE archive_entries SET received_at=now()-interval '100 days' WHERE id=$1", id); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := pool.UserTx(ctx, tenant, user)
	if err != nil {
		t.Fatal(err)
	}
	queries := store.New(tx)
	grant, err := queries.CreateComplianceGrant(ctx, store.CreateComplianceGrantParams{TenantID: tenant, UserID: user, Scope: "retention_manage", Reason: "Explicit destructive retention approval", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}, GrantedBy: pgtype.Int8{Int64: user, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := queries.PreviewArchiveRetention(ctx, store.PreviewArchiveRetentionParams{TenantID: tenant, RetentionDays: 30, CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	if err = queries.ConfirmArchiveRetentionPreview(ctx, store.ConfirmArchiveRetentionPreviewParams{TenantID: tenant, ID: preview.ID}); err != nil {
		t.Fatal(err)
	}
	if err = queries.ActivateArchiveRetention(ctx, store.ActivateArchiveRetentionParams{TenantID: tenant, RetentionDays: 30, CreatedBy: pgtype.Int8{Int64: user, Valid: true}, ConfirmedPreview: preview.ID, AuthorizationGrantID: pgtype.Int8{Int64: grant.ID, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// First pass: retention soft-delete transition. The held copy must stay.
	if err = w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	state := func(id int64) (deleted, purged bool, path string) {
		t.Helper()
		const q = "SELECT a.deleted_at IS NOT NULL, c.purged_at IS NOT NULL, a.path FROM archive_entries a JOIN archive_content c ON c.entry_id=a.id AND c.tenant_id=a.tenant_id WHERE a.id=$1"
		if err := pool.QueryRow(ctx, q, id).Scan(&deleted, &purged, &path); err != nil {
			t.Fatal(err)
		}
		return deleted, purged, path
	}
	var heldPath string
	heldDeleted, heldPurged, _ := state(heldID)
	if heldDeleted || heldPurged {
		t.Fatalf("legal hold did not block retention: deleted=%v purged=%v", heldDeleted, heldPurged)
	}
	if freeDeleted, _, _ := state(freeID); !freeDeleted {
		t.Fatal("unheld copy was not expired by the same pass")
	}
	// Second pass: backdate the soft-deleted copy into the physical purge
	// window; the held copy must still never become a purge candidate.
	if _, err = pool.Exec(ctx, "UPDATE archive_entries SET deleted_at=now()-interval '31 days' WHERE id=$1", freeID); err != nil {
		t.Fatal(err)
	}
	if err = w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	heldDeleted, heldPurged, heldPath = state(heldID)
	if heldDeleted || heldPurged {
		t.Fatalf("held copy entered cleanup: deleted=%v purged=%v", heldDeleted, heldPurged)
	}
	if _, err = os.Stat(heldPath); err != nil {
		t.Fatal("held original missing", err)
	}
	if _, freePurged, _ := state(freeID); !freePurged {
		t.Fatal("unheld copy was not purged")
	}
}

// ARC-002: the hold state and its recall guard data live in the PostgreSQL
// dump itself: a restored database re-derives the same protection without any
// out-of-band state.
func TestLegalHoldAndRecallSurviveRestoreFromDump(t *testing.T) {
	pgDump, err := exec.LookPath("pg_dump")
	if err != nil {
		t.Skip("pg_dump not installed; skipping restore test")
	}
	psql, err := exec.LookPath("psql")
	if err != nil {
		t.Skip("psql not installed; skipping restore test")
	}
	ctx := context.Background()
	source := testdb.New(t)
	m, err := db.NewMigrator(source)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	pool, err := db.OpenPlatform(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var id int64
		if err = pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('restore','Restore') RETURNING id")
	user := scalar("INSERT INTO users(email,password_hash) VALUES('restore@example.test','fixture-only') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Restore','restore','active') RETURNING id", brand)
	domain := scalar("INSERT INTO domains(tenant_id,fqdn,status,provisioning_state,verification_token,verified_at) VALUES($1,'restore.test','active','ready',$2,now()) RETURNING id", tenant, strings.Repeat("b", 64))
	mailbox := scalar("INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'held','held@restore.test','fixture-only','active') RETURNING id", tenant, domain)
	entry := scalar("INSERT INTO archive_entries(tenant_id,mailbox_id,message_id,path,received_at,raw_sha256,legal_hold) VALUES($1,$2,'<restore@example.test>','fixture',now(),$3,true) RETURNING id", tenant, mailbox, strings.Repeat("c", 64))
	recall := scalar("INSERT INTO recall_requests(tenant_id,message_id,requested_by,archive_entry_id,mailbox_id) VALUES($1,'<restore@example.test>',$2,$3,$4) RETURNING id", tenant, user, entry, mailbox)

	dumpPath := filepath.Join(t.TempDir(), "dump.sql")
	if out, dumpErr := exec.CommandContext(context.Background(), pgDump, "-f", dumpPath, source).CombinedOutput(); dumpErr != nil {
		t.Fatalf("pg_dump failed: %v %s", dumpErr, out)
	}
	// A new-machine restore replays the dump into an empty database, not a
	// bootstrapped one: ownership and grants travel inside the dump itself.
	adminConfig, err := pgx.ParseConfig(source)
	if err != nil {
		t.Fatal(err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	targetName := fmt.Sprintf("urmail_restore_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{targetName}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = admin.Exec(cleanupCtx, "DROP DATABASE "+pgx.Identifier{targetName}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(cleanupCtx)
	})
	parsed, err := url.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + targetName
	params := parsed.Query()
	params.Del("role")
	parsed.RawQuery = params.Encode()
	target := parsed.String()
	if out, restoreErr := exec.CommandContext(context.Background(), psql, "-v", "ON_ERROR_STOP=1", "-q", "-f", dumpPath, target).CombinedOutput(); restoreErr != nil {
		t.Fatalf("restore failed: %v %s", restoreErr, out)
	}
	restored, err := db.OpenPlatform(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var hold bool
	if err = restored.QueryRow(ctx, "SELECT legal_hold FROM archive_entries WHERE tenant_id=$1 AND id=$2", tenant, entry).Scan(&hold); err != nil {
		t.Fatal("held copy missing after restore", err)
	}
	if !hold {
		t.Fatal("legal hold lost in restore")
	}
	var recallStatus string
	if err = restored.QueryRow(ctx, "SELECT status FROM recall_requests WHERE tenant_id=$1 AND id=$2", tenant, recall).Scan(&recallStatus); err != nil {
		t.Fatal("recall request missing after restore", err)
	}
	if recallStatus != "pending" {
		t.Fatalf("recall state changed in restore: %q", recallStatus)
	}
	// The restored guard still resolves the hold for a sibling recall: the
	// agent path re-reads the hold from the restored database only.
	var conflict bool
	const guard = "SELECT count(*) > 0 FROM archive_entries WHERE tenant_id=$1 AND mailbox_id=$2 AND message_id=$3 AND id<>$4 AND legal_hold AND deleted_at IS NULL"
	if err = restored.QueryRow(ctx, guard, tenant, mailbox, "<restore@example.test>", entry).Scan(&conflict); err != nil {
		t.Fatal(err)
	}
	if conflict {
		t.Fatal("unexpected second copy in fixture")
	}
}

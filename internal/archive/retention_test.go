package archive

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/db"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func retentionDatabase(t *testing.T) (*db.Pool, int64, int64, int64) {
	t.Helper()
	ctx := context.Background()
	dsn := testdb.New(t)
	m, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err = m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	pool, err := db.OpenPlatform(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	scalar := func(sql string, args ...any) int64 {
		var id int64
		if e := pool.QueryRow(ctx, sql, args...).Scan(&id); e != nil {
			t.Fatal(e)
		}
		return id
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('retention','Retention') RETURNING id")
	user := scalar("INSERT INTO users(email,password_hash) VALUES('retention@example.test','hash') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Retention','retention','active') RETURNING id", brand)
	if _, err = pool.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", user, tenant); err != nil {
		t.Fatal(err)
	}
	domain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'retention.test','active',now(),'token') RETURNING id", tenant)
	mailbox := scalar("INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status) VALUES($1,$2,'alice','alice@retention.test','hash',1024,'active') RETURNING id", tenant, domain)
	return pool, tenant, user, mailbox
}
func retentionCopy(t *testing.T, w *Worker, tenant, mailbox int64, id string) int64 {
	t.Helper()
	_, err := Spool(w.Spool, domainMail.ArchiveRecipient{TenantID: tenant, MailboxID: mailbox, Address: "alice@retention.test"}, bytes.NewBufferString("Message-ID: <"+id+"@retention.test>\r\n\r\nrecoverable"))
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	var entry int64
	if err = w.Pool.QueryRow(context.Background(), "SELECT id FROM archive_entries WHERE tenant_id=$1 AND message_id=$2", tenant, "<"+id+"@retention.test>").Scan(&entry); err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestPolicyRevocationCancelsUnexecutedPurgeAndCopyCanRecover(t *testing.T) {
	pool, tenant, user, mailbox := retentionDatabase(t)
	ctx := context.Background()
	q := store.New(pool.Pool)
	w := &Worker{Pool: pool, Spool: filepath.Join(t.TempDir(), "spool"), Archive: filepath.Join(t.TempDir(), "archive")}
	id := retentionCopy(t, w, tenant, mailbox, "policy")
	if _, err := pool.Exec(ctx, "UPDATE archive_entries SET received_at=now()-interval '100 days' WHERE id=$1", id); err != nil {
		t.Fatal(err)
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
	if err = w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "UPDATE archive_entries SET deleted_at=now()-interval '31 days' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	// A claim committed just before revocation must be canceled if unlink has not run.
	if err = q.PlatformBeginArchivePurge(ctx, store.PlatformBeginArchivePurgeParams{TenantID: tenant, EntryID: id}); err != nil {
		t.Fatal(err)
	}
	tx, err = pool.UserTx(ctx, tenant, user)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.New(tx).UpsertRetentionPolicy(ctx, store.UpsertRetentionPolicyParams{TenantID: tenant, RetentionDays: 30, Action: "soft_delete", Active: false, CreatedBy: pgtype.Int8{Int64: user, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	var path string
	var claim, purged bool
	if err = pool.QueryRow(ctx, "SELECT a.path,c.purge_started_at IS NOT NULL,c.purged_at IS NOT NULL FROM archive_entries a JOIN archive_content c ON c.entry_id=a.id WHERE a.id=$1", id).Scan(&path, &claim, &purged); err != nil {
		t.Fatal(err)
	}
	if claim || purged {
		t.Fatalf("revoked policy continued cleanup: claim=%v purged=%v", claim, purged)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("revoked policy lost original", err)
	}
	restored, err := q.RestoreArchiveCopy(ctx, store.RestoreArchiveCopyParams{TenantID: tenant, ID: id})
	if err != nil || restored != 1 {
		t.Fatalf("intact original could not restore: %d %v", restored, err)
	}
	if err = w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	var deleted bool
	if err = pool.QueryRow(ctx, "SELECT deleted_at IS NOT NULL FROM archive_entries WHERE id=$1", id).Scan(&deleted); err != nil || deleted {
		t.Fatal("disabled policy deleted restored copy", err)
	}
}

func TestGrantRevocationCancelsClaimedPurgeAndRestoredCopyStaysIntact(t *testing.T) {
	pool, tenant, user, mailbox := retentionDatabase(t)
	ctx := context.Background()
	q := store.New(pool.Pool)
	w := &Worker{Pool: pool, Spool: filepath.Join(t.TempDir(), "spool"), Archive: filepath.Join(t.TempDir(), "archive")}
	id := retentionCopy(t, w, tenant, mailbox, "policy")
	if _, err := pool.Exec(ctx, "UPDATE archive_entries SET received_at=now()-interval '100 days' WHERE id=$1", id); err != nil {
		t.Fatal(err)
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
	if err = w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "UPDATE archive_entries SET deleted_at=now()-interval '31 days' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	// A claim committed just before revocation must be canceled if unlink has not run.
	if err = q.PlatformBeginArchivePurge(ctx, store.PlatformBeginArchivePurgeParams{TenantID: tenant, EntryID: id}); err != nil {
		t.Fatal(err)
	}
	tx, err = pool.UserTx(ctx, tenant, user)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.New(tx).RevokeComplianceGrant(ctx, store.RevokeComplianceGrantParams{TenantID: tenant, ID: grant.ID, RevokedBy: pgtype.Int8{Int64: user, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	var path string
	var claim, purged bool
	if err = pool.QueryRow(ctx, "SELECT a.path,c.purge_started_at IS NOT NULL,c.purged_at IS NOT NULL FROM archive_entries a JOIN archive_content c ON c.entry_id=a.id WHERE a.id=$1", id).Scan(&path, &claim, &purged); err != nil {
		t.Fatal(err)
	}
	if claim || purged {
		t.Fatalf("revoked grant continued cleanup: claim=%v purged=%v", claim, purged)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("revoked grant lost original", err)
	}
	restored, err := q.RestoreArchiveCopy(ctx, store.RestoreArchiveCopyParams{TenantID: tenant, ID: id})
	if err != nil || restored != 1 {
		t.Fatalf("intact original could not restore: %d %v", restored, err)
	}
	if err = w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	var deleted bool
	if err = pool.QueryRow(ctx, "SELECT deleted_at IS NOT NULL FROM archive_entries WHERE id=$1", id).Scan(&deleted); err != nil || deleted {
		t.Fatal("revoked grant deleted restored copy", err)
	}
}

func TestHoldAndPhysicalPurgeSerializeWithoutFalseHold(t *testing.T) {
	pool, tenant, _, mailbox := retentionDatabase(t)
	ctx := context.Background()
	q := store.New(pool.Pool)
	w := &Worker{Pool: pool, Spool: filepath.Join(t.TempDir(), "spool"), Archive: filepath.Join(t.TempDir(), "archive")}
	for i := 0; i < 5; i++ {
		id := retentionCopy(t, w, tenant, mailbox, time.Now().Format("150405.000000000"))
		if _, err := pool.Exec(ctx, "UPDATE archive_entries SET deleted_at=now()-interval '31 days' WHERE id=$1", id); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		var holdErr, purgeErr error
		go func() {
			defer wg.Done()
			_, holdErr = q.SetArchiveEntryLegalHold(ctx, store.SetArchiveEntryLegalHoldParams{TenantID: tenant, ID: id})
		}()
		go func() { defer wg.Done(); purgeErr = w.purge(ctx, tenant, id) }()
		wg.Wait()
		if purgeErr != nil {
			t.Fatal(purgeErr)
		}
		var held, purged bool
		var path string
		if err := pool.QueryRow(ctx, "SELECT a.legal_hold,c.purged_at IS NOT NULL,a.path FROM archive_entries a JOIN archive_content c ON c.entry_id=a.id WHERE a.id=$1", id).Scan(&held, &purged, &path); err != nil {
			t.Fatal(err)
		}
		if held {
			if purged || holdErr != nil {
				t.Fatalf("false hold succeeded: held=%v purged=%v error=%v", held, purged, holdErr)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("held original absent", err)
			}
		} else if !purged || holdErr == nil {
			t.Fatalf("unserialized outcome: held=%v purged=%v holdErr=%v", held, purged, holdErr)
		}
	}
}

func TestDuplicateCopyRepairsMissingOriginalButNeverOverwritesCorruption(t *testing.T) {
	pool, tenant, _, mailbox := retentionDatabase(t)
	ctx := context.Background()
	w := &Worker{Pool: pool, Spool: filepath.Join(t.TempDir(), "spool"), Archive: filepath.Join(t.TempDir(), "archive")}
	id := retentionCopy(t, w, tenant, mailbox, "repair")
	var original string
	if err := pool.QueryRow(ctx, "SELECT path FROM archive_entries WHERE id=$1", id).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	recipient := domainMail.ArchiveRecipient{TenantID: tenant, MailboxID: mailbox, Address: "alice@retention.test"}
	source := "Message-ID: <repair@retention.test>\r\n\r\nrecoverable"
	manifest, err := Spool(w.Spool, recipient, bytes.NewBufferString(source))
	if err != nil {
		t.Fatal(err)
	}
	if err = w.IndexSpoolCopy(ctx, manifest.ID); err != nil {
		t.Fatal(err)
	}
	recovered, err := os.ReadFile(original)
	if err != nil || string(recovered) != source {
		t.Fatal("missing original did not recover from verified duplicate", err)
	}
	if err = os.WriteFile(original, []byte("corrupted forensic original"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err = Spool(w.Spool, recipient, bytes.NewBufferString(source))
	if err != nil {
		t.Fatal(err)
	}
	if err = w.IndexSpoolCopy(ctx, manifest.ID); err == nil {
		t.Fatal("corruption silently overwritten")
	}
	if retained, err := os.ReadFile(filepath.Join(w.Spool, manifest.ID, "raw.eml")); err != nil || string(retained) != source {
		t.Fatal("new good copy discarded while old original was corrupt", err)
	}
	if corrupted, err := os.ReadFile(original); err != nil || string(corrupted) != "corrupted forensic original" {
		t.Fatal("forensic corruption evidence overwritten", err)
	}
}

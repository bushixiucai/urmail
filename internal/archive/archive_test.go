package archive

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bushixiucai/urmail/internal/db"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestSpoolSurvivesUnavailableDatabaseAndDetectsTampering(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	manifest, err := Spool(root, domainMail.ArchiveRecipient{TenantID: 1, MailboxID: 2, Address: "alice@example.test"}, bytes.NewBufferString("Subject: Durable\r\n\r\noriginal"))
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	got, raw, err := readManifest(dir, manifest.ID)
	if err != nil || got.SHA256 != manifest.SHA256 || len(raw) == 0 {
		t.Fatalf("lost durable original: %+v %v", got, err)
	}
	if err = os.WriteFile(filepath.Join(root, manifest.ID, "raw.eml"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = readManifest(dir, manifest.ID); err == nil {
		t.Fatal("tampered original accepted")
	}
}

func TestMIMEPlainTextAndAttachmentHashesWithoutActiveHTML(t *testing.T) {
	raw := []byte("Subject: =?UTF-8?B?5rWL6K+V?=\r\nMessage-ID: <mime@example.test>\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\nContent-Type: text/plain\r\n\r\nsearchable body\r\n--x\r\nContent-Type: text/html\r\n\r\n<script>bad()</script>\r\n--x\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=\"../../evil.html\"\r\nContent-Transfer-Encoding: base64\r\n\r\naGVsbG8=\r\n--x--\r\n")
	content := ExtractMIME(raw)
	if content.Error != "" || content.Subject != "测试" || content.Body != "searchable body\n" || len(content.Attachments) != 1 || content.Attachments[0].Size != 5 || len(content.Attachments[0].SHA256) != 64 {
		t.Fatalf("bad MIME result: %+v", content)
	}
}

func TestArchiveReconciliationDedupePinnedOwnershipAndHoldPurge(t *testing.T) {
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
	defer pool.Close()
	var brand, tenant, domain, mailbox int64
	for _, step := range []struct {
		query string
		args  []any
		id    *int64
	}{
		{"INSERT INTO brands(slug,name) VALUES('archive','Archive') RETURNING id", nil, &brand},
		{"INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Archive','archive','active') RETURNING id", []any{&brand}, &tenant},
	} {
		args := step.args
		if len(args) > 0 {
			args = []any{brand}
		}
		if err = pool.QueryRow(ctx, step.query, args...).Scan(step.id); err != nil {
			t.Fatal(err)
		}
	}
	if err = pool.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'archive.test','active',now(),'token') RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status) VALUES($1,$2,'alice','alice@archive.test','hash',1024,'active') RETURNING id", tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	worker := &Worker{Pool: pool, Spool: filepath.Join(t.TempDir(), "spool"), Archive: filepath.Join(t.TempDir(), "archive")}
	recipient := domainMail.ArchiveRecipient{TenantID: tenant, MailboxID: mailbox, Address: "alice@archive.test"}
	for _, body := range []string{"first", "first", "different"} {
		if _, err = Spool(worker.Spool, recipient, bytes.NewBufferString("Message-ID: <same@archive.test>\r\nSubject: Search\r\n\r\n"+body)); err != nil {
			t.Fatal(err)
		}
	}
	if err = worker.Once(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM archive_entries WHERE tenant_id=$1", tenant).Scan(&count); err != nil || count != 2 {
		t.Fatalf("same bytes duplicate or different original lost: %d %v", count, err)
	}
	var held, unheld int64
	if err = pool.QueryRow(ctx, "SELECT min(id),max(id) FROM archive_entries").Scan(&held, &unheld); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "UPDATE archive_entries SET legal_hold=(id=$1),received_at=now()-interval '100 days'", held); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "UPDATE archive_entries SET deleted_at=now()-interval '31 days' WHERE id=$1", held); err == nil {
		t.Fatal("hold allowed soft-delete")
	}
	if _, err = pool.Exec(ctx, "UPDATE archive_entries SET deleted_at=now()-interval '31 days' WHERE id=$1", unheld); err != nil {
		t.Fatal(err)
	}
	if err = worker.Once(ctx); err != nil {
		t.Fatal(err)
	}
	var purged bool
	if err = pool.QueryRow(ctx, "SELECT purged_at IS NOT NULL FROM archive_content WHERE entry_id=$1", unheld).Scan(&purged); err != nil || !purged {
		t.Fatalf("eligible original not purged: %v %v", purged, err)
	}
	if _, err = pool.Exec(ctx, "UPDATE archive_entries SET legal_hold=true WHERE id=$1", unheld); err == nil {
		t.Fatal("physically purged bytes accepted hold")
	}
	if _, err = pool.Exec(ctx, "INSERT INTO retention_policies(tenant_id,retention_days,action,active) VALUES($1,1,'soft_delete',true)", tenant); err == nil {
		t.Fatal("unconfirmed retention enabled")
	}
	// A delayed forged/reused address cannot alter immutable mailbox binding.
	wrong := recipient
	wrong.MailboxID++
	if _, err = Spool(worker.Spool, wrong, bytes.NewBufferString("Subject: Wrong\r\n\r\nnever rebind")); err != nil {
		t.Fatal(err)
	}
	if err = worker.Once(ctx); err == nil {
		t.Fatal("wrong pinned identity accepted")
	}
}

func TestSpoolRefusesUnsafeOrUnavailableStorage(t *testing.T) {
	root := t.TempDir()
	unsafe := filepath.Join(root, "unsafe")
	if err := os.Mkdir(unsafe, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Spool(unsafe, domainMail.ArchiveRecipient{TenantID: 1, MailboxID: 1, Address: "a@example.test"}, bytes.NewBufferString("body")); err == nil {
		t.Fatal("publicly readable archive spool accepted")
	}
	unavailable := filepath.Join(root, "file")
	if err := os.WriteFile(unavailable, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Spool(unavailable, domainMail.ArchiveRecipient{TenantID: 1, MailboxID: 1, Address: "a@example.test"}, bytes.NewBufferString("body")); err == nil {
		t.Fatal("unavailable persistence was acknowledged")
	}
	target := filepath.Join(root, "private")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Spool(link, domainMail.ArchiveRecipient{TenantID: 1, MailboxID: 1, Address: "a@example.test"}, bytes.NewBufferString("body")); err == nil {
		t.Fatal("symlink archive root accepted")
	}
}

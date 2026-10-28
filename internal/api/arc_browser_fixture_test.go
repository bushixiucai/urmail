package api_test

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api"
	archivestore "github.com/bushixiucai/urmail/internal/archive"
	domainmail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// TestArchiveInvestigationBrowserFixture serves production auth/CSRF/API and
// private real archive originals for an explicitly enabled loopback browser run.
func TestArchiveInvestigationBrowserFixture(t *testing.T) {
	directory := os.Getenv("URMAIL_ARC_BROWSER_DIR")
	if directory == "" {
		t.Skip("explicit private browser fixture required")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	platform, app := testdb.OpenMigrated(t)
	ctx := t.Context()
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := platform.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('default','Archive Browser') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Archive Browser','arc-browser','active') RETURNING id", brand)
	accounts := make([]map[string]any, 0, 3)
	users := make([]int64, 0, 3)
	for _, name := range []string{"owner", "requester", "approver"} {
		password := "ArchiveBrowser-" + uuid.NewString() + "!"
		hash, err := domainmail.HashPassword(password)
		if err != nil {
			t.Fatal(err)
		}
		email := "arc-browser-" + name + "@example.test"
		user := scalar("INSERT INTO users(email,password_hash) VALUES($1,$2) RETURNING id", email, hash)
		role := "admin"
		if name == "owner" {
			role = "owner"
		}
		if _, err = platform.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,$3)", user, tenant, role); err != nil {
			t.Fatal(err)
		}
		users = append(users, user)
		accounts = append(accounts, map[string]any{"name": name, "email": email, "password": password, "id": user})
	}
	for _, u := range users[1:] {
		for _, scope := range []string{"full_content", "export"} {
			tx, err := app.UserTx(ctx, tenant, users[0])
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.New(tx).CreateComplianceGrant(ctx, store.CreateComplianceGrantParams{TenantID: tenant, UserID: u, Scope: scope, Reason: "Explicit independent browser investigation", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true}, GrantedBy: pgtype.Int8{Int64: users[0], Valid: true}})
			if err == nil {
				err = tx.Commit(ctx)
			}
			_ = tx.Rollback(ctx)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	domain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'arc-browser.test','active',now(),'fixture') RETURNING id", tenant)
	mailbox := scalar("INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'alice','alice@arc-browser.test','hash','active') RETURNING id", tenant, domain)
	archiveRoot := filepath.Join(directory, "archive")
	worker := &archivestore.Worker{Pool: platform, Spool: filepath.Join(directory, "spool"), Archive: archiveRoot}
	raw := "Message-ID: <arc-browser-original@example.test>\r\nSubject: Browser approved original\r\nContent-Type: multipart/mixed; boundary=arc\r\n\r\n--arc\r\nContent-Type: text/plain\r\n\r\nOriginal browser evidence body\r\n--arc\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=proof.bin\r\nContent-Transfer-Encoding: base64\r\n\r\nAQIDBA==\r\n--arc--\r\n"
	if _, err := archivestore.Spool(worker.Spool, domainmail.ArchiveRecipient{TenantID: tenant, MailboxID: mailbox, Address: "alice@arc-browser.test"}, strings.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if err := worker.Once(ctx); err != nil {
		t.Fatal(err)
	}
	entry := scalar("SELECT id FROM archive_entries WHERE tenant_id=$1", tenant)
	catalog, err := i18n.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.New(api.Options{Logger: zap.NewNop(), Messages: catalog, AppPool: app, PlatformPool: platform, Audit: store.New(platform.Pool), ArchiveRoot: archiveRoot, Version: "arc-browser"}))
	defer server.Close()
	fixture, _ := json.Marshal(map[string]any{"base_url": server.URL, "accounts": accounts, "tenant_id": tenant, "entry_id": entry})
	if err = os.WriteFile(filepath.Join(directory, "fixture.json"), fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Log("Ready: real auth/CSRF/PG/archive fixture; waiting for private browser.done")
	deadline := time.Now().Add(8 * time.Minute)
	finished := false
	for time.Now().Before(deadline) {
		if result, e := os.ReadFile(filepath.Join(directory, "browser.done")); e == nil {
			if strings.TrimSpace(string(result)) != "PASS" {
				t.Fatal("browser reported failure")
			}
			finished = true
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !finished {
		t.Fatal("browser fixture timed out")
	}
	facts := map[string]int64{
		"source_hold_survives_closure": scalar("SELECT count(*) FROM archive_entries WHERE tenant_id=$1 AND legal_hold", tenant),
		"closed_case":                  scalar("SELECT count(*) FROM archive_investigations WHERE tenant_id=$1 AND status='closed'", tenant),
		"independent_approval":         scalar("SELECT count(*) FROM archive_approval_requests WHERE tenant_id=$1 AND state='approved' AND requester_user_id<>approver_user_id", tenant),
		"one_manifest":                 scalar("SELECT count(*) FROM archive_evidence_exports WHERE tenant_id=$1", tenant),
		"successful_download_custody":  scalar("SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action='archive.evidence_transfer_finished' AND detail->>'completed'='true'", tenant),
	}
	for name, count := range facts {
		if count != 1 {
			t.Fatalf("browser closure %s=%d", name, count)
		}
	}
	output, _ := json.MarshalIndent(facts, "", "  ")
	if err = os.WriteFile(filepath.Join(directory, "closure-facts.json"), output, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Log("PASS real browser investigation -> membership -> source -> distinct approval -> immutable manifest -> ZIP download -> case close")
}

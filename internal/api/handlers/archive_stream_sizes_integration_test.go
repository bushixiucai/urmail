package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	archiveStore "github.com/bushixiucai/urmail/internal/archive"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
)

// archivePayloadReader produces bounded chunks without allocating the message.
type archivePayloadReader struct{}

func (archivePayloadReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

// TestArchiveExportLayeredSizesOverRealHTTP verifies the complete transaction,
// grant, audit and deferred-stream boundary through an actual HTTP connection.
// Generation and client hashing are streamed; no recorder retains large bodies.
func TestArchiveExportLayeredSizesOverRealHTTP(t *testing.T) {
	platform, app := authIntegrationDatabase(t)
	ctx := t.Context()
	scalar := func(query string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := platform.QueryRow(ctx, query, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('archive-layers','Archive Layers') RETURNING id")
	user := scalar("INSERT INTO users(email,password_hash) VALUES('archive-layers@example.test','hash') RETURNING id")
	// ARC-001/PD-112: the grant creator must differ from the beneficiary.
	grantor := scalar("INSERT INTO users(email,password_hash) VALUES('archive-layers-grantor@example.test','hash') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Layers','archive-layers','active') RETURNING id", brand)
	if _, err := platform.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner'),($3,$4,'owner')", user, tenant, grantor, tenant); err != nil {
		t.Fatal(err)
	}
	domain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'archive-layers.test','active',now(),'token') RETURNING id", tenant)
	mailbox := scalar("INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status) VALUES($1,$2,'alice','alice@archive-layers.test','hash',1024,'active') RETURNING id", tenant, domain)
	tx, err := app.UserTx(ctx, tenant, grantor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.New(tx).CreateComplianceGrant(ctx, store.CreateComplianceGrantParams{TenantID: tenant, UserID: user, Scope: "export", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}, Reason: "layered export integrity verification", GrantedBy: pgtype.Int8{Int64: grantor, Valid: true}}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, mib := range []int64{16, 64, 128} {
		t.Run(fmt.Sprintf("%dMiB", mib), func(t *testing.T) {
			requestCtx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			root := t.TempDir()
			source, err := os.OpenFile(filepath.Join(root, "source.eml"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			size := mib << 20
			header := fmt.Sprintf("Message-ID: <archive-layer-%d@example.test>\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=layer.bin\r\n\r\n", mib)
			digest := sha256.New()
			writer := io.MultiWriter(source, digest)
			if _, err = io.WriteString(writer, header); err != nil {
				t.Fatal(err)
			}
			if _, err = io.CopyN(writer, archivePayloadReader{}, size-int64(len(header))); err != nil {
				t.Fatal(err)
			}
			expected := hex.EncodeToString(digest.Sum(nil))
			if _, err = source.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			worker := &archiveStore.Worker{Pool: platform, Spool: filepath.Join(root, "spool"), Archive: filepath.Join(root, "archive")}
			manifest, err := archiveStore.Spool(worker.Spool, domainMail.ArchiveRecipient{TenantID: tenant, MailboxID: mailbox, Address: "alice@archive-layers.test"}, source)
			if err != nil {
				t.Fatal(err)
			}
			if manifest.Size != size || manifest.SHA256 != expected {
				t.Fatal("streamed fixture spool changed original bytes")
			}
			if err = worker.IndexSpoolCopy(requestCtx, manifest.ID); err != nil {
				t.Fatal(err)
			}
			entry := scalar("SELECT id FROM archive_entries WHERE tenant_id=$1 AND raw_sha256=$2", tenant, expected)
			handler := &ArchiveContentHandler{Store: store.New(app.Pool), Root: worker.Archive}
			router := gin.New()
			router.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, tenant); c.Set(middleware.ContextUserID, user) })
			router.Use(middleware.Audit(store.New(platform.Pool), zap.NewNop()))
			router.Use(middleware.RequireTenantTx(app, zap.NewNop()))
			router.GET("/api/v1/archive/:id/export", handler.Export)
			server := httptest.NewServer(router)
			defer server.Close()
			client := server.Client()
			client.Timeout = 2 * time.Minute
			request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, server.URL+"/api/v1/archive/"+strconv.FormatInt(entry, 10)+"/export", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK || response.ContentLength != size || response.Header.Get("Content-Type") != "message/rfc822" || response.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("invalid streamed response: status=%d length=%d headers=%v", response.StatusCode, response.ContentLength, response.Header)
			}
			actualDigest := sha256.New()
			copied, err := io.CopyBuffer(actualDigest, response.Body, make([]byte, 64<<10))
			actual := hex.EncodeToString(actualDigest.Sum(nil))
			if err != nil || copied != size || actual != expected {
				t.Fatalf("HTTP export changed/truncated original: size=%d want=%d sha256=%s want=%s err=%v", copied, size, actual, expected, err)
			}
			var audited bool
			if err = platform.QueryRow(requestCtx, "SELECT EXISTS(SELECT 1 FROM audit_logs WHERE tenant_id=$1 AND action='READ archive.export' AND target_id=$2)", tenant, strconv.FormatInt(entry, 10)).Scan(&audited); err != nil || !audited {
				t.Fatalf("mandatory durable export audit missing: %t %v", audited, err)
			}
			_ = response.Body.Close()
			server.Close()
			if len(archiveTransferSlots) != 0 {
				t.Fatal("completed real HTTP stream retained its transfer permit")
			}
			t.Logf("PASS real PG/grant/transaction/audit + HTTP streamed export size=%d bytes SHA256=%s", copied, actual)
		})
	}
}

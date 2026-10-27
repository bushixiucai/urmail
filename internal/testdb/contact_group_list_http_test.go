package testdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/handlers"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func TestGroupListHTTPHidesDeleted(t *testing.T) {
	platform, _ := OpenMigrated(t)
	ctx := t.Context()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, domain, mailbox, book int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('listh','ListH') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'L','listh-l','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'listh.test','active','tok',now()) RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'a','a@listh.test','x','active') RETURNING id", tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_addressbooks(tenant_id,mailbox_id,name) VALUES($1,$2,'Contacts') RETURNING id", tenant, mailbox).Scan(&book); err != nil {
		t.Fatal(err)
	}
	q := store.New(tx)
	live, err := q.PlatformInsertContactGroup(ctx, store.PlatformInsertContactGroupParams{TenantID: tenant, MailboxID: mailbox, ID: book, Name: "Live"})
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := q.PlatformInsertContactGroup(ctx, store.PlatformInsertContactGroupParams{TenantID: tenant, MailboxID: mailbox, ID: book, Name: "Hidden"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "UPDATE mailbox_contact_groups SET deleted_at=now() WHERE id=$1", hidden.ID); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := handlers.MailboxContactsHandler{Groups: q}
	r.GET("/groups", func(c *gin.Context) {
		c.Set(middleware.ContextMailboxSession, webmail.Session{TenantID: tenant, MailboxID: mailbox})
		h.ListGroups(c)
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/groups", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "Hidden") || !strings.Contains(w.Body.String(), "Live") {
		t.Fatal(w.Code, w.Body.String())
	}
	_ = live
}

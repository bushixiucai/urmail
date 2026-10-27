package testdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/handlers"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func TestGroupMemberHTTPWritesOnlySameAddressBook(t *testing.T) {
	platform, _ := OpenMigrated(t)
	ctx := t.Context()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, domain, mailbox, book, other, contact, group, otherGroup int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('httpg','HTTP') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'H','http-h','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'http.test','active','tok',now()) RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'a','a@http.test','x','active') RETURNING id", tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_addressbooks(tenant_id,mailbox_id,name) VALUES($1,$2,'One') RETURNING id", tenant, mailbox).Scan(&book); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_addressbooks(tenant_id,mailbox_id,name) VALUES($1,$2,'Two') RETURNING id", tenant, mailbox).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_contacts(tenant_id,mailbox_id,addressbook_id,display_name) VALUES($1,$2,$3,'Ada') RETURNING id", tenant, mailbox, book).Scan(&contact); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_contact_groups(tenant_id,mailbox_id,addressbook_id,name) VALUES($1,$2,$3,'A') RETURNING id", tenant, mailbox, book).Scan(&group); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailbox_contact_groups(tenant_id,mailbox_id,addressbook_id,name) VALUES($1,$2,$3,'B') RETURNING id", tenant, mailbox, other).Scan(&otherGroup); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := handlers.MailboxContactsHandler{GroupStore: store.New(tx)}
	r.POST("/members", func(c *gin.Context) {
		c.Set(middleware.ContextMailboxSession, webmail.Session{TenantID: tenant, MailboxID: mailbox})
		h.AddGroupMember(c)
	})
	post := func(body string) int {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/members", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	if code := post(`{"group_addressbook_id":` + strconv.FormatInt(other, 10) + `,"contact_addressbook_id":` + strconv.FormatInt(book, 10) + `,"group_id":` + strconv.FormatInt(otherGroup, 10) + `,"contact_id":` + strconv.FormatInt(contact, 10) + `}`); code != http.StatusBadRequest {
		t.Fatal(code)
	}
	if code := post(`{"group_addressbook_id":` + strconv.FormatInt(book, 10) + `,"contact_addressbook_id":` + strconv.FormatInt(book, 10) + `,"group_id":` + strconv.FormatInt(group, 10) + `,"contact_id":` + strconv.FormatInt(contact, 10) + `}`); code != http.StatusCreated {
		t.Fatal(code)
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM mailbox_contact_group_members WHERE tenant_id=$1 AND mailbox_id=$2 AND group_id=$3", tenant, mailbox, group).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

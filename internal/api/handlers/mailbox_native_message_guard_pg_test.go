package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/mailboxsend"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func TestGenericDraftMutationsRespectSubmissionReceiptPG(t *testing.T) {
	fixture := newNativeAIHTTPFixture(t)
	session := webmail.Session{TenantID: fixture.principal.TenantID, MailboxID: fixture.principal.MailboxID, Address: fixture.principal.Address}
	imap := &managedFolderFixture{fakeIMAPClient: &fakeIMAPClient{folders: []webmail.Folder{{Name: "INBOX"}, {Name: "Drafts", Flags: []string{`\Drafts`}}, {Name: "Archive"}, {Name: "Trash", Flags: []string{`\Trash`}}}}}
	vault := webmail.NewMemoryVault()
	vault.Put("token", "password") //nolint:staticcheck // test adapter seeding, Put exists for this
	h := &MailboxNativeHandler{Enabled: true, Pool: fixture.pool, Auth: fakeMailboxAuth{}, Sessions: &fakeMailboxSessions{}, Vault: vault, Dialer: &fakeDialer{wantAddress: session.Address, client: imap}}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.ContextMailboxSession, session)
		c.Set(middleware.ContextMailboxTokenHash, "token")
	})
	router.POST("/folders/:name/messages/copy", h.CopyMessages)
	router.POST("/folders/:name/messages/trash", h.TrashMessages)
	router.POST("/folders/:name/messages/move", h.MoveMessages)
	router.POST("/folders/:name/messages/delete", h.DeleteMessages)
	router.POST("/folders/:name/messages/flags", h.FlagMessages)
	_, err := fixture.pool.Exec(t.Context(), `INSERT INTO mailbox_send_receipts(tenant_id,mailbox_id,request_key,request_hash,message_id,raw_secret,draft_folder,draft_uid,draft_uidvalidity)VALUES($1,$2,'source-mutation-000001',repeat('a',64),'<guard@example.test>','enc:v1:fixture','Drafts',42,9)`, session.TenantID, session.MailboxID)
	if err != nil {
		t.Fatal(err)
	}
	request := func(action, key, body string) *httptest.ResponseRecorder {
		requestContext, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		req := httptest.NewRequestWithContext(requestContext, http.MethodPost, "/folders/Drafts/messages/"+action, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	for index, state := range []struct{ status, copy string }{{"prepared", "none"}, {"submitting", "none"}, {"uncertain", "none"}, {"accepted", "pending"}, {"accepted", "unknown"}, {"accepted", "held"}} {
		_, err = fixture.pool.Exec(t.Context(), `UPDATE mailbox_send_receipts SET status=$3,sent_copy_state=$4 WHERE tenant_id=$1 AND mailbox_id=$2`, session.TenantID, session.MailboxID, state.status, state.copy)
		if err != nil {
			t.Fatal(err)
		}
		for _, action := range []string{"copy", "trash", "move", "delete", "flags"} {
			body := `{"uids":[42],"uid_validity":9,"to":"Archive","expunge":true,"add":["\\Deleted"]}`
			out := request(action, fmt.Sprintf("guard-case-%02d-%s", index, action), body)
			if out.Code != http.StatusConflict || !strings.Contains(out.Body.String(), "DRAFT_SEND_LOCKED") {
				t.Fatalf("%+v %s: %d %s", state, action, out.Code, out.Body.String())
			}
		}
	}
	if imap.mutations != 0 || len(imap.deleted) > 0 || len(imap.moved) > 0 || len(imap.flagged) > 0 {
		t.Fatal("locked draft reached an IMAP mutation")
	}
	// Explicit deletion is safe only after its immutable Sent copy is complete.
	_, err = fixture.pool.Exec(t.Context(), `UPDATE mailbox_send_receipts SET status='accepted',sent_copy_state='complete' WHERE tenant_id=$1 AND mailbox_id=$2`, session.TenantID, session.MailboxID)
	if err != nil {
		t.Fatal(err)
	}
	out := request("delete", "guard-complete-delete", `{"uids":[42],"uid_validity":9,"expunge":true}`)
	if out.Code != http.StatusOK || len(imap.deleted) != 1 {
		t.Fatalf("completed copy cleanup: %d %s", out.Code, out.Body.String())
	}
	// A source lock and durable transfer journal also work with one connection.
	config := fixture.pool.Config().Copy()
	config.MaxConns = 1
	config.MinConns = 0
	single, openErr := pgxpool.NewWithConfig(t.Context(), config)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer single.Close()
	h.Pool = &db.Pool{Pool: single}
	copied := request("copy", "guard-single-connection", `{"uids":[43],"uid_validity":9,"to":"Archive"}`)
	if copied.Code != http.StatusOK || imap.mutations != 1 {
		t.Fatalf("single-connection transfer: %d %s", copied.Code, copied.Body.String())
	}
	// A concurrent prepared submission wins under the same transaction lock.
	_, err = fixture.pool.Exec(t.Context(), `UPDATE mailbox_send_receipts SET status='rejected' WHERE tenant_id=$1 AND mailbox_id=$2`, session.TenantID, session.MailboxID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.pool.MailboxTx(t.Context(), session.TenantID, session.MailboxID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = mailboxsend.LockDraft(t.Context(), store.New(tx), session.TenantID, session.MailboxID, mailboxsend.DraftRef{Folder: "Drafts", UID: 42, Validity: 9}); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `UPDATE mailbox_send_receipts SET status='prepared',sent_copy_state='none' WHERE tenant_id=$1 AND mailbox_id=$2`, session.TenantID, session.MailboxID); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- request("move", "guard-concurrent-move", `{"uids":[42],"uid_validity":9,"to":"Archive"}`)
	}()
	select {
	case result := <-done:
		t.Fatalf("mutation bypassed in-flight submission lock: %d %s", result.Code, result.Body.String())
	case <-time.After(100 * time.Millisecond):
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.Code != http.StatusConflict {
			t.Fatalf("mutation ignored committed submission: %d %s", result.Code, result.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mutation did not resume after lock release")
	}
}

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/mailboxsend"
	"github.com/bushixiucai/urmail/internal/store"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/testdb"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func durableNativeReceiptFixture(t *testing.T) (*MailboxNativeHandler, *gin.Engine, string, *fakeSubmitter) {
	t.Helper()
	p, _ := testdb.OpenMigrated(t)
	ctx := t.Context()
	var tenant, mailbox, domain int64
	if e := p.QueryRow(ctx, `WITH b AS(INSERT INTO brands(slug,name)VALUES('http-receipt','Receipt')RETURNING id),t AS(INSERT INTO tenants(brand_id,name,slug,status)SELECT id,'Receipt','http-receipt','active' FROM b RETURNING id)SELECT id FROM t`).Scan(&tenant); e != nil {
		t.Fatal(e)
	}
	if e := p.QueryRow(ctx, `INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at)VALUES($1,'http-receipt.test','active','proof',now())RETURNING id`, tenant).Scan(&domain); e != nil {
		t.Fatal(e)
	}
	if e := p.QueryRow(ctx, `INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes)VALUES($1,$2,'alice','alice@http-receipt.test','fixture',1024)RETURNING id`, tenant, domain).Scan(&mailbox); e != nil {
		t.Fatal(e)
	}
	token := "http-receipt-fixture-token"
	hash := mailboxprincipal.HashToken(token)
	sessions := &fakeMailboxSessions{byHash: map[string]webmail.Session{hash: {TenantID: tenant, MailboxID: mailbox, Address: "alice@http-receipt.test", TokenHash: hash, ExpiresAt: time.Now().Add(time.Hour)}}}
	vault := webmail.NewMemoryVault()
	vault.Put(hash, "fixture-password")
	box, e := secrets.New(bytes.Repeat([]byte{3}, 32))
	if e != nil {
		t.Fatal(e)
	}
	submit := &fakeSubmitter{}
	h := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{}, Sessions: sessions, Pool: p, Secrets: box, Vault: vault, Submitter: submit, Dialer: &fakeDialer{err: errors.New("IMAP temporarily unavailable")}}
	router := newNativeTestRouter(t, h, sessions)
	receipts := router.Group("/api/v1/mailbox")
	receipts.Use(middleware.RequireMailboxAuth(sessions))
	receipts.GET("/send-receipts", h.ListSendReceipts)
	receipts.GET("/send-receipts/:id", h.GetSendReceipt)
	receipts.POST("/send-receipts/:id/repair-sent-copy", h.RepairSendReceiptCopy)
	return h, router, token, submit
}
func durableSendRequest(t *testing.T, r *gin.Engine, token, key, subject string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(mailboxSendRequest{To: []string{"recipient@example.test"}, Subject: subject, Text: "fixture body"})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/mailbox/send", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}
func TestImmediateSMTPAcceptanceSurvivesIMAPFailureAndRequestReplay(t *testing.T) {
	h, r, token, submit := durableNativeReceiptFixture(t)
	w := durableSendRequest(t, r, token, "accepted-http-send-0001", "original")
	if w.Code != 200 || submit.calls != 1 {
		t.Fatalf("status=%d SMTP=%d body=%s", w.Code, submit.calls, w.Body.String())
	}
	var result struct {
		Sent    bool `json:"sent"`
		Receipt struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
			Copy   string `json:"sent_copy_state"`
		} `json:"receipt"`
	}
	decoder := json.NewDecoder(w.Body)
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatal("accepted response contains appended error/JSON")
	}
	if !result.Sent || result.Receipt.Status != "accepted" || result.Receipt.ID == 0 || result.Receipt.Copy != "pending" {
		t.Fatalf("accepted state lost: %+v", result)
	}
	w = durableSendRequest(t, r, token, "accepted-http-send-0001", "original")
	if w.Code != 200 || submit.calls != 1 {
		t.Fatal("replay repeated SMTP")
	}
	w = durableSendRequest(t, r, token, "accepted-http-send-0001", "changed")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "IDEMPOTENCY_CONFLICT") || submit.calls != 1 {
		t.Fatalf("changed request accepted %d %s", w.Code, w.Body.String())
	}
	w = durableSendRequest(t, r, token, "", "missing key")
	if w.Code != 400 || submit.calls != 1 {
		t.Fatal("missing key reached SMTP")
	}
	h.MaySend = func(context.Context, int64, int64) (bool, error) { return false, nil }
	w = durableSendRequest(t, r, token, "suspended-http-send-0001", "blocked")
	if w.Code != 403 || !strings.Contains(w.Body.String(), "MAIL_SENDING_SUSPENDED") || submit.calls != 1 {
		t.Fatal("suspended sender reached SMTP")
	}
}
func TestImmediateUncertainReceiptIsReturnedAndCannotReplaySMTP(t *testing.T) {
	_, r, token, submit := durableNativeReceiptFixture(t)
	submit.err = webmail.ErrSubmitUncertain
	w := durableSendRequest(t, r, token, "uncertain-http-send-0001", "original")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"uncertain"`) || !strings.Contains(w.Body.String(), `"sent":false`) {
		t.Fatalf("uncertain outcome not explicit: %d %s", w.Code, w.Body.String())
	}
	w = durableSendRequest(t, r, token, "uncertain-http-send-0001", "original")
	if w.Code != 200 || submit.calls != 1 {
		t.Fatal("uncertain delivery retried")
	}
}

func TestSubmittedDraftCannotAutosaveDiscardOrEnterAnotherSend(t *testing.T) {
	h, router, token, submit := durableNativeReceiptFixture(t)
	ctx := t.Context()
	session, e := h.Sessions.Get(ctx, mailboxprincipal.HashToken(token))
	if e != nil {
		t.Fatal(e)
	}
	client := &draftJournalIMAP{fakeIMAPClient: &fakeIMAPClient{folders: []webmail.Folder{{Name: "Drafts", Flags: []string{`\Drafts`}}}, message: &webmail.Message{MessageSummary: webmail.MessageSummary{UID: 42}, UIDValidity: 9}}, raw: map[uint32][]byte{42: []byte("source draft")}, next: 100}
	h.Dialer = &fakeDialer{wantAddress: session.Address, client: client}
	out := webmail.FreezeOutgoing(webmail.Outgoing{From: session.Address, To: []string{"recipient@example.test"}, Text: "fixture"})
	out.Raw = []byte(webmail.BuildMIME(out.From, out))
	service := h.sendReceiptService()
	row, _, e := service.Prepare(ctx, session.TenantID, session.MailboxID, "locked-draft-send-0001", strings.Repeat("a", 64), "fixture-password", out, mailboxsend.DraftRef{Folder: "Drafts", UID: 42, Validity: 9}, 0)
	if e != nil {
		t.Fatal(e)
	}
	row, e = service.Submit(ctx, row, session.Address, "fixture-password", out)
	if e != nil {
		t.Fatal(e)
	}
	for _, state := range []string{"accepted", "uncertain"} {
		if _, e = h.Pool.Exec(ctx, `UPDATE mailbox_send_receipts SET status=$1 WHERE id=$2`, state, row.ID); e != nil {
			t.Fatal(e)
		}
		for _, action := range []struct{ method, path, body string }{{"PUT", "/api/v1/mailbox/drafts", `{"uid":42,"uid_validity":9,"text":"overwrite submitted draft"}`}, {"DELETE", "/api/v1/mailbox/drafts/42?uid_validity=9", ""}, {"POST", "/api/v1/mailbox/send", `{"to":["recipient@example.test"],"text":"resend","draft_source":{"folder_enc":"Drafts","uid":42,"uid_validity":9}}`}} {
			req := httptest.NewRequestWithContext(ctx, action.method, action.path, strings.NewReader(action.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Idempotency-Key", "locked-draft-new-0001")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != 409 || !strings.Contains(w.Body.String(), "DRAFT_SEND_LOCKED") {
				t.Fatalf("%s %s status=%d body=%s", state, action.method, w.Code, w.Body.String())
			}
		}
	}
	if submit.calls != 1 || client.appends != 0 || client.raw[42] == nil {
		t.Fatal("submitted source was mutated or resent")
	}
	// A confirmed copy permits explicit cleanup even when automatic deletion failed.
	if _, e = h.Pool.Exec(ctx, `UPDATE mailbox_send_receipts SET status='accepted',sent_copy_state='complete',raw_secret='',smtp_secret='' WHERE id=$1`, row.ID); e != nil {
		t.Fatal(e)
	}
	query := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, "GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	w := query("/api/v1/mailbox/send-receipts?folder_enc=Drafts&uid=42&uid_validity=9")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"sent_copy_state":"complete"`) || strings.Contains(w.Body.String(), "raw_secret") || strings.Contains(w.Body.String(), "smtp_secret") {
		t.Fatalf("source recovery metadata invalid: %d %s", w.Code, w.Body.String())
	}
	w = query("/api/v1/mailbox/send-receipts?folder_enc=Drafts&uid=42&uid_validity=10")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatal("stale source generation matched receipt")
	}
	w = query("/api/v1/mailbox/send-receipts?limit=1&offset=0")
	if w.Code != 200 || !strings.Contains(w.Body.String(), row.RequestKey) {
		t.Fatal("receipt history not recoverable")
	}
	req := httptest.NewRequestWithContext(ctx, "DELETE", "/api/v1/mailbox/drafts/42?uid_validity=9", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 || client.raw[42] != nil || submit.calls != 1 {
		t.Fatalf("completed source could not be explicitly deleted: %d %s", w.Code, w.Body.String())
	}
}

func TestScheduledUncertainAcknowledgementRequiresOwnerAndConfirmation(t *testing.T) {
	h, _, token, _ := durableNativeReceiptFixture(t)
	ctx := t.Context()
	session, e := h.Sessions.Get(ctx, mailboxprincipal.HashToken(token))
	if e != nil {
		t.Fatal(e)
	}
	q := store.New(h.Pool.Pool)
	row, e := q.PlatformCreateMailboxScheduledSend(ctx, store.PlatformCreateMailboxScheduledSendParams{TenantID: session.TenantID, MailboxID: session.MailboxID, DraftFolder: "Drafts", DraftUid: 42, DraftUidvalidity: 9, SendAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}, SmtpSecret: "sealed-fixture", IdempotencyKey: "acknowledgement-fixture-0001", RequestHash: "fixture"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = h.Pool.Exec(ctx, `UPDATE mailbox_scheduled_sends SET status='uncertain',sent_raw_secret='enc:v1:retained-fixture' WHERE id=$1`, row.ID); e != nil {
		t.Fatal(e)
	}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(middleware.ContextMailboxSession, session) })
	router.POST("/ack/:id", h.AcknowledgeUncertainScheduledSend)
	request := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, "POST", "/ack/"+strconv.FormatInt(row.ID, 10), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	if w := request(`{"confirmed":false}`); w.Code != 400 {
		t.Fatal("unconfirmed acknowledgement succeeded")
	}
	session.TenantID += 100
	if w := request(`{"confirmed":true}`); w.Code != 409 {
		t.Fatal("another tenant released draft")
	}
	session.TenantID -= 100
	if w := request(`{"confirmed":true}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"`+canceledScheduleStatus+`"`) {
		t.Fatalf("owner release failed %d %s", w.Code, w.Body.String())
	}
	current, e := q.PlatformGetMailboxScheduledSend(ctx, store.PlatformGetMailboxScheduledSendParams{TenantID: session.TenantID, MailboxID: session.MailboxID, ID: row.ID})
	if e != nil || current.SmtpSecret != "" || current.SentRawSecret != "" {
		t.Fatal("acknowledged attempt retained credentials/material")
	}
	locked, e := q.PlatformDraftSendLocked(ctx, store.PlatformDraftSendLockedParams{TenantID: session.TenantID, MailboxID: session.MailboxID, DraftFolder: "Drafts", DraftUid: 42, DraftUidvalidity: 9})
	if e != nil || locked.Bool {
		t.Fatal("explicitly released draft remains locked")
	}
	if _, e = h.Pool.Exec(ctx, `UPDATE mailbox_scheduled_sends SET status='sent' WHERE id=$1`, row.ID); e != nil {
		t.Fatal(e)
	}
	if w := request(`{"confirmed":true}`); w.Code != 409 {
		t.Fatal("accepted mail became resendable")
	}
	if _, e = h.Pool.Exec(ctx, `UPDATE mailbox_scheduled_sends SET status='held' WHERE id=$1`, row.ID); e != nil {
		t.Fatal(e)
	}
	if w := request(`{"confirmed":true}`); w.Code != 200 {
		t.Fatal("owner-reviewed held attempt cannot end")
	}
}

func TestImmediateDraftSourceCannotDeleteInboxMessage(t *testing.T) {
	h, router, token, submit := durableNativeReceiptFixture(t)
	session, e := h.Sessions.Get(t.Context(), mailboxprincipal.HashToken(token))
	if e != nil {
		t.Fatal(e)
	}
	client := &fakeIMAPClient{message: &webmail.Message{MessageSummary: webmail.MessageSummary{UID: 42}, UIDValidity: 9}}
	h.Dialer = &fakeDialer{wantAddress: session.Address, client: client}
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/mailbox/send", strings.NewReader(`{"to":["recipient@example.test"],"text":"fixture","draft_source":{"folder_enc":"INBOX","uid":42,"uid_validity":9}}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "non-draft-source-0001")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 409 || submit.calls != 0 || len(client.deleted) != 0 {
		t.Fatalf("non-draft source reached SMTP/deletion status=%d calls=%d", w.Code, submit.calls)
	}
}

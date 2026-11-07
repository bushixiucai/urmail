package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/mailboxschedule"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// TestMailboxNativeRuntime runs only in the disposable, network-isolated Linux
// harness. Authentication and scheduling use real PostgreSQL; IMAP/SMTP/LMTP use
// real Dovecot/Postfix. No service client or delivery transport is mocked.
func TestMailboxNativeRuntime(t *testing.T) {
	if os.Getenv("URMAIL_WEBMAIL_RUNTIME") != "1" {
		t.Skip("requires scripts/native-webmail-protocol-acceptance.py")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	p, _ := testdb.OpenMigrated(t)
	password := "isolated-fixture-password"
	hash, err := domainMail.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	var tenant, domain int64
	if err = p.QueryRow(ctx, `WITH b AS(INSERT INTO brands(slug,name) VALUES('native-runtime','Native runtime') RETURNING id),t AS(INSERT INTO tenants(brand_id,name,slug,status) SELECT id,'Native runtime','native-runtime','active' FROM b RETURNING id) SELECT id FROM t`).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, `INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'example.test','active','fixture',now()) RETURNING id`, tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "bob", "hidden"} {
		if _, err = p.Exec(ctx, `INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes) VALUES($1,$2,$3,$4,$5,104857600)`, tenant, domain, name, name+"@example.test", hash); err != nil {
			t.Fatal(err)
		}
	}
	q := store.New(p.Pool)
	principal := StoreMailboxNative{Q: q}
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	dialer := webmail.NetDialer{Addr: "127.0.0.1:993", Timeout: 5 * time.Second}
	submitter := webmail.NetSubmitter{Addr: "127.0.0.1:587", Timeout: 5 * time.Second}
	h := &MailboxNativeHandler{Enabled: true, Auth: principal, Sessions: principal, Vault: webmail.NewMemoryVault(), Dialer: dialer, Submitter: submitter, Secrets: box, Pool: p}
	router := newNativeTestRouter(t, h, principal)
	authRoutes := router.Group("/api/v1/mailbox")
	authRoutes.Use(middleware.RequireMailboxAuth(principal))
	authRoutes.POST("/scheduled-sends", h.CreateScheduledSend)
	authRoutes.GET("/scheduled-sends", h.ListScheduledSends)
	authRoutes.DELETE("/scheduled-sends/:id", h.CancelScheduledSend)
	server := httptest.NewServer(router)
	defer server.Close()
	token := ""
	request := func(method, path string, body any, key string) (int, []byte) {
		t.Helper()
		var encoded []byte
		if body != nil {
			encoded, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req, e := http.NewRequestWithContext(ctx, method, server.URL+"/api/v1/mailbox"+path, bytes.NewReader(encoded))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, e := io.ReadAll(resp.Body)
		if e != nil {
			t.Fatal(e)
		}
		return resp.StatusCode, raw
	}
	status, raw := request(http.MethodPost, "/session", map[string]string{"email": "alice@example.test", "password": password}, "")
	var session struct {
		Token string `json:"token"`
	}
	if status != http.StatusOK || json.Unmarshal(raw, &session) != nil || session.Token == "" {
		t.Fatalf("login status=%d", status)
	}
	token = session.Token
	subject := fmt.Sprintf("native-api-runtime-%d", time.Now().UnixNano())
	attachment := bytes.Repeat([]byte("native-attachment-"), 512)
	compose := map[string]any{"to": []string{"bob@example.test"}, "bcc": []string{"hidden@example.test"}, "subject": subject, "text": "plain draft", "html": "<p>HTML draft</p>", "attachments": []map[string]string{{"filename": "runtime.bin", "content_type": "application/octet-stream", "content_base64": base64.StdEncoding.EncodeToString(attachment)}}}
	status, raw = request(http.MethodPut, "/drafts", compose, "native-runtime-draft-first")
	var draft webmail.DraftSaveResult
	if status != http.StatusOK || json.Unmarshal(raw, &draft) != nil || draft.UID == 0 || draft.UIDValidity == 0 {
		t.Fatalf("save status=%d body=%s", status, raw)
	}
	compose["uid"], compose["uid_validity"] = draft.UID, draft.UIDValidity
	compose["text"] = "updated plain draft"
	status, raw = request(http.MethodPut, "/drafts", compose, "native-runtime-draft-replace")
	if status != http.StatusOK || json.Unmarshal(raw, &draft) != nil || draft.UID == 0 {
		t.Fatalf("replace status=%d body=%s", status, raw)
	}
	sendAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	intention := map[string]any{"draft_uid": draft.UID, "draft_uidvalidity": draft.UIDValidity, "send_at": sendAt.Format(time.RFC3339)}
	key := "native-runtime-request-0001"
	status, raw = request(http.MethodPost, "/scheduled-sends", intention, key)
	var scheduled struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}
	if status != http.StatusOK || json.Unmarshal(raw, &scheduled) != nil || scheduled.ID == 0 {
		t.Fatalf("schedule status=%d body=%s", status, raw)
	}
	status, raw = request(http.MethodPost, "/scheduled-sends", intention, key)
	var replay struct {
		ID int64 `json:"id"`
	}
	if status != http.StatusOK || json.Unmarshal(raw, &replay) != nil || replay.ID != scheduled.ID {
		t.Fatal("schedule replay did not preserve task")
	}
	// Advance only this owned test row; the original requested send_at stays intact.
	if _, err = p.Exec(ctx, `UPDATE mailbox_scheduled_sends SET next_attempt_at=now()-interval '1 second' WHERE tenant_id=$1 AND id=$2`, tenant, scheduled.ID); err != nil {
		t.Fatal(err)
	}
	// A fresh worker with no memory credential vault proves durable seal recovery.
	worker := mailboxschedule.NewWorker(mailboxschedule.Options{Pool: p, Secrets: box, Dialer: dialer, Submitter: submitter})
	worked, err := worker.Step(ctx)
	if err != nil || !worked {
		t.Fatalf("worker worked=%v error=%v", worked, err)
	}
	var rowState, credential string
	var original pgtype.Timestamptz
	if err = p.QueryRow(ctx, `SELECT status,smtp_secret,send_at FROM mailbox_scheduled_sends WHERE tenant_id=$1 AND id=$2`, tenant, scheduled.ID).Scan(&rowState, &credential, &original); err != nil {
		t.Fatal(err)
	}
	if rowState != "sent" || credential != "" || !original.Time.Equal(sendAt) {
		t.Fatalf("unsafe final state=%s", rowState)
	}
	if worked, err = worker.Step(ctx); err != nil || worked {
		t.Fatal("completed delivery became runnable again", err)
	}
	for _, recipient := range []string{"bob@example.test", "hidden@example.test"} {
		client, e := dialer.Dial(ctx, recipient, password)
		if e != nil {
			t.Fatal(e)
		}
		var found webmail.ListResult
		deadline := time.Now().Add(15 * time.Second)
		for {
			found, e = client.ListMessages(ctx, "INBOX", webmail.ListOpts{Query: "subject:" + subject})
			if e != nil {
				_ = client.Close()
				t.Fatal(e)
			}
			if found.Total > 0 || time.Now().After(deadline) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if found.Total != 1 {
			_ = client.Close()
			t.Fatalf("recipient delivery count=%d", found.Total)
		}
		message, e := client.GetMessage(ctx, "INBOX", found.Messages[0].UID)
		if e != nil {
			_ = client.Close()
			t.Fatal(e)
		}
		if message.Text != "updated plain draft" || message.HTML != "<p>HTML draft</p>" || len(message.Bcc) != 0 || len(message.Attachments) != 1 {
			_ = client.Close()
			t.Fatal("message content or envelope continuity failed")
		}
		_, _, actual, e := client.GetAttachment(ctx, "INBOX", message.UID, message.Attachments[0].PartID)
		if e != nil || sha256.Sum256(actual) != sha256.Sum256(attachment) {
			_ = client.Close()
			t.Fatal("attachment digest mismatch", e)
		}
		_ = client.Close()
	}
	status, raw = request(http.MethodGet, "/scheduled-sends", nil, "")
	if status != http.StatusOK || !bytes.Contains(raw, []byte(`"status":"sent"`)) {
		t.Fatal("terminal result not visible")
	}
	// Replaying a successful request after the draft was expunged still returns it.
	status, raw = request(http.MethodPost, "/scheduled-sends", intention, key)
	if status != http.StatusOK || !bytes.Contains(raw, []byte(`"status":"sent"`)) {
		t.Fatal("terminal idempotency replay failed")
	}
	// Real immediate-send recovery shares the receipt key and never enters SMTP twice.
	for _, loseAppendAck := range []bool{false, true} {
		attemptKey := fmt.Sprintf("native-immediate-request-%t", loseAppendAck)
		attemptSubject := subject + fmt.Sprintf("-immediate-%t", loseAppendAck)
		fault := &runtimeCopyFaultDialer{base: dialer, failConnect: !loseAppendAck, dropAck: loseAppendAck}
		h.Dialer = fault
		immediate := map[string]any{"to": []string{"bob@example.test"}, "bcc": []string{"hidden@example.test"}, "subject": attemptSubject, "text": "immediate receipt body"}
		status, raw = request(http.MethodPost, "/send", immediate, attemptKey)
		var result struct {
			Sent    bool `json:"sent"`
			Receipt struct {
				ID        int64  `json:"id"`
				Status    string `json:"status"`
				Copy      string `json:"sent_copy_state"`
				MessageID string `json:"message_id"`
			} `json:"receipt"`
		}
		if status != 200 || json.Unmarshal(raw, &result) != nil || !result.Sent || result.Receipt.Status != "accepted" || result.Receipt.Copy == "complete" {
			t.Fatalf("accepted copy failure returned invalid outcome: %d %s", status, raw)
		}
		// Only this isolated receipt is advanced past its copy backoff.
		if _, err = p.Exec(ctx, `UPDATE mailbox_send_receipts SET next_attempt_at=now()-interval '1 second' WHERE tenant_id=$1 AND id=$2`, tenant, result.Receipt.ID); err != nil {
			t.Fatal(err)
		}
		if worked, err = worker.Step(ctx); err != nil || !worked {
			t.Fatalf("immediate copy repair: worked=%v err=%v", worked, err)
		}
		row, e := q.PlatformGetSendReceipt(ctx, store.PlatformGetSendReceiptParams{TenantID: tenant, MailboxID: sessionMailboxID(ctx, t, principal, token), ID: result.Receipt.ID})
		if e != nil || row.SentCopyState != "complete" || row.RawSecret != "" || row.SmtpSecret != "" {
			t.Fatal("immediate copy recovery did not complete", e)
		}
		status, raw = request(http.MethodPost, "/send", immediate, attemptKey)
		if status != 200 || !bytes.Contains(raw, []byte(`"sent_copy":"saved"`)) {
			t.Fatal("immediate successful key replay changed outcome")
		}
		for _, address := range []string{"alice@example.test", "bob@example.test", "hidden@example.test"} {
			client, e := dialer.Dial(ctx, address, password)
			if e != nil {
				t.Fatal(e)
			}
			folder := "INBOX"
			if address == "alice@example.test" {
				folders, e := client.ListFolders(ctx)
				if e != nil {
					t.Fatal(e)
				}
				folder = webmail.ResolveSentFolder(folders)
			}
			var listed webmail.ListResult
			until := time.Now().Add(15 * time.Second)
			for {
				listed, e = client.ListMessages(ctx, folder, webmail.ListOpts{Query: "subject:" + attemptSubject})
				if e != nil {
					t.Fatal(e)
				}
				if listed.Total > 0 || time.Now().After(until) {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if listed.Total != 1 {
				t.Fatalf("SMTP replay or duplicate Sent: address=%s count=%d", address, listed.Total)
			}
			message, e := client.GetMessage(ctx, folder, listed.Messages[0].UID)
			if e != nil {
				t.Fatal(e)
			}
			if message.MessageID != result.Receipt.MessageID || message.Date == nil || len(message.Bcc) != 0 {
				t.Fatal("Date/Message-ID/Bcc identity drift")
			}
			_ = client.Close()
		}
	}
	h.Dialer = dialer
	t.Log("real PG + HTTP + SMTP/LMTP scheduled and immediate receipts; lost IMAP connection and APPEND ACK; exactly one delivery and Sent copy; stable Date/Message-ID/Bcc; recovery secret cleanup passed")
}

// runtimeCopyFaultDialer exercises real SMTP/IMAP with an injected lost copy acknowledgement.
type runtimeCopyFaultDialer struct {
	base                 webmail.Dialer
	failConnect, dropAck bool
}

func (d *runtimeCopyFaultDialer) Dial(ctx context.Context, address, password string) (webmail.Client, error) {
	if d.failConnect {
		d.failConnect = false
		return nil, errors.New("isolated fixture IMAP outage")
	}
	client, err := d.base.Dial(ctx, address, password)
	if err != nil {
		return nil, err
	}
	if d.dropAck {
		d.dropAck = false
		return &runtimeLostCopyAckClient{Client: client}, nil
	}
	return client, nil
}

type runtimeLostCopyAckClient struct{ webmail.Client }

func (c *runtimeLostCopyAckClient) FindSentCopy(ctx context.Context, raw []byte) (webmail.DraftSaveResult, bool, error) {
	return c.Client.(webmail.SentCopyFinder).FindSentCopy(ctx, raw)
}
func (c *runtimeLostCopyAckClient) SaveSentCopy(ctx context.Context, raw []byte) (webmail.DraftSaveResult, error) {
	result, err := c.Client.SaveSentCopy(ctx, raw)
	if err == nil {
		err = webmail.ErrSentCopyPending
	}
	return result, err
}

func sessionMailboxID(ctx context.Context, t *testing.T, principal StoreMailboxNative, token string) int64 {
	t.Helper()
	session, err := principal.Get(ctx, mailboxprincipal.HashToken(token))
	if err != nil {
		t.Fatal(err)
	}
	return session.MailboxID
}

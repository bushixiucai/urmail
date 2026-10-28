package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
	"github.com/bushixiucai/urmail/internal/webmail"
)

const canceledScheduleStatus = "cancelled" //nolint:misspell // Keep the existing API status.

type scheduleHTTPDialer struct{}

func (scheduleHTTPDialer) Dial(context.Context, string, string) (webmail.Client, error) {
	return &fakeIMAPClient{folders: []webmail.Folder{{Name: "Drafts", Flags: []string{`\Drafts`}}}, message: &webmail.Message{MessageSummary: webmail.MessageSummary{UID: 42, Subject: "scheduled"}, To: []webmail.Address{{Email: "recipient@example.test"}}, Text: "body"}}, nil
}

func TestScheduleHTTPIdempotencyCancellationAndIsolation(t *testing.T) {
	p, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	q := store.New(p.Pool)
	var tenant, mailbox int64
	if err := p.QueryRow(ctx, `WITH b AS(INSERT INTO brands(slug,name)VALUES('schedule-http','Schedule')RETURNING id),t AS(INSERT INTO tenants(brand_id,name,slug,status)SELECT id,'Schedule','schedule-http','active' FROM b RETURNING id)SELECT id FROM t`).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	var domain int64
	if err := p.QueryRow(ctx, `INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at)VALUES($1,'schedule.example.test','active','fixture',now())RETURNING id`, tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes)VALUES($1,$2,'alice','alice@schedule.example.test','fixture',1024)RETURNING id`, tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	token := "schedule-http-token"
	hash := mailboxprincipal.HashToken(token)
	sessions := &fakeMailboxSessions{byHash: map[string]webmail.Session{hash: {TokenHash: hash, TenantID: tenant, MailboxID: mailbox, Address: "alice@schedule.example.test", ExpiresAt: time.Now().Add(time.Hour)}}}
	vault := webmail.NewMemoryVault()
	vault.Put(hash, "fixture-password")
	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	h := &MailboxNativeHandler{Enabled: true, Pool: p, Vault: vault, Secrets: box, Dialer: scheduleHTTPDialer{}, Auth: fakeMailboxAuth{}, Sessions: sessions}
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	a := r.Group("/api/v1/mailbox")
	a.Use(middleware.RequireMailboxAuth(sessions))
	a.POST("/scheduled-sends", h.CreateScheduledSend)
	a.GET("/scheduled-sends", h.ListScheduledSends)
	a.DELETE("/scheduled-sends/:id", h.CancelScheduledSend)
	a.POST("/scheduled-sends/:id/repair-sent-copy", h.RepairScheduledSentCopy)
	request := func(method, path, key string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, method, path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	sendAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	body := []byte(fmt.Sprintf(`{"draft_uid":42,"draft_uidvalidity":9,"send_at":%q}`, sendAt))
	if w := request(http.MethodPost, "/api/v1/mailbox/scheduled-sends", "", body); w.Code != http.StatusBadRequest {
		t.Fatalf("missing key %d", w.Code)
	}
	key := "schedule-request-0001"
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- request(http.MethodPost, "/api/v1/mailbox/scheduled-sends", key, body)
		}()
	}
	wg.Wait()
	close(responses)
	var id int64
	for w := range responses {
		if w.Code != http.StatusOK {
			t.Fatalf("concurrent create %d %s", w.Code, w.Body.String())
		}
		var item struct {
			ID int64 `json:"id"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		if id != 0 && id != item.ID {
			t.Fatal("idempotent request created multiple rows")
		}
		id = item.ID
	}
	var count int
	if err = p.QueryRow(ctx, `SELECT count(*) FROM mailbox_scheduled_sends`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	changed := []byte(fmt.Sprintf(`{"draft_uid":42,"draft_uidvalidity":9,"send_at":%q}`, time.Now().Add(2*time.Hour).UTC().Format(time.RFC3339)))
	if w := request(http.MethodPost, "/api/v1/mailbox/scheduled-sends", key, changed); w.Code != http.StatusConflict {
		t.Fatalf("payload mismatch %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodPost, "/api/v1/mailbox/scheduled-sends", "schedule-other-key", body); w.Code != http.StatusConflict {
		t.Fatalf("active draft conflict %d %s", w.Code, w.Body.String())
	}
	wrong := []byte(fmt.Sprintf(`{"draft_uid":42,"draft_uidvalidity":10,"send_at":%q}`, sendAt))
	if w := request(http.MethodPost, "/api/v1/mailbox/scheduled-sends", "schedule-wrong-validity", wrong); w.Code != http.StatusConflict {
		t.Fatalf("changed draft %d %s", w.Code, w.Body.String())
	}
	if _, err = p.Exec(ctx, `UPDATE mailbox_scheduled_sends SET next_attempt_at=now() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	claim, err := q.PlatformClaimMailboxScheduledSend(ctx)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/mailbox/scheduled-sends/" + strconv.FormatInt(id, 10)
	if w := request(http.MethodDelete, path, "", nil); w.Code != http.StatusOK {
		t.Fatalf("cancel preparing %d %s", w.Code, w.Body.String())
	}
	if _, err = q.PlatformBeginMailboxScheduledSendSubmission(ctx, store.PlatformBeginMailboxScheduledSendSubmissionParams{ID: id, ClaimToken: claim.ClaimToken}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("canceled claim submitted", err)
	}
	if w := request(http.MethodDelete, path, "", nil); w.Code != http.StatusOK {
		t.Fatal("cancel replay failed", w.Code)
	}
	// Completed and canceled outcomes remain visible in the mailbox list.
	if w := request(http.MethodGet, "/api/v1/mailbox/scheduled-sends", "", nil); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(strconv.Quote(canceledScheduleStatus))) {
		t.Fatalf("terminal list %d %s", w.Code, w.Body.String())
	}
	// Replaying the original key never creates a new intention, even after cancel.
	if w := request(http.MethodPost, "/api/v1/mailbox/scheduled-sends", key, body); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(strconv.Quote(canceledScheduleStatus))) {
		t.Fatalf("replay after cancel %d %s", w.Code, w.Body.String())
	}
	w := request(http.MethodPost, "/api/v1/mailbox/scheduled-sends", "schedule-request-0002", body)
	if w.Code != 200 {
		t.Fatalf("new task %d %s", w.Code, w.Body.String())
	}
	if _, err = p.Exec(ctx, `UPDATE mailbox_scheduled_sends SET next_attempt_at=now() WHERE status='pending'`); err != nil {
		t.Fatal(err)
	}
	claim, err = q.PlatformClaimMailboxScheduledSend(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.PlatformBeginMailboxScheduledSendSubmission(ctx, store.PlatformBeginMailboxScheduledSendSubmissionParams{ID: claim.ID, ClaimToken: claim.ClaimToken}); err != nil {
		t.Fatal(err)
	}
	if w = request(http.MethodDelete, "/api/v1/mailbox/scheduled-sends/"+strconv.FormatInt(claim.ID, 10), "", nil); w.Code != http.StatusConflict {
		t.Fatalf("cancel submitting %d %s", w.Code, w.Body.String())
	}
	// Releasing machine quarantine must not let a fresh request bypass a held intention.
	if _, err = p.Exec(ctx, `UPDATE mailbox_scheduled_sends SET status='held',smtp_secret='',claim_token=claim_token+1 WHERE id=$1`, claim.ID); err != nil {
		t.Fatal(err)
	}
	if w = request(http.MethodPost, "/api/v1/mailbox/scheduled-sends", "schedule-bypass-held", body); w.Code != http.StatusConflict {
		t.Fatalf("held task bypass %d %s", w.Code, w.Body.String())
	}

	// A fresh owner's IMAP session can repair a restored accepted copy after the
	// original scheduled credential was removed. No SMTP submitter is configured.
	if _, err = p.Exec(ctx, `UPDATE mailbox_scheduled_sends SET status='sent',sent_at=now(),sent_copy_state='held',sent_raw=$2,smtp_secret='',lease_until=NULL WHERE id=$1`, claim.ID, []byte("Message-ID: <repair-http@fixture>\r\n\r\naccepted")); err != nil {
		t.Fatal(err)
	}
	h.Dialer = sentRepairHTTPDialer{}
	repairPath := "/api/v1/mailbox/scheduled-sends/" + strconv.FormatInt(claim.ID, 10) + "/repair-sent-copy"
	if w = request(http.MethodPost, repairPath, "", nil); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"sent_copy_state":"complete"`)) {
		t.Fatalf("fresh owner repair %d %s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("accepted")) || bytes.Contains(w.Body.Bytes(), []byte("sent_raw")) {
		t.Fatal("private repair payload leaked into response")
	}
	if w = request(http.MethodPost, repairPath, "", nil); w.Code != 200 {
		t.Fatalf("completed repair replay %d", w.Code)
	}
	// Mailbox authentication still requires exact tenant/mailbox scope.
	sessions.byHash[hash] = webmail.Session{TokenHash: hash, TenantID: tenant + 1, MailboxID: mailbox, Address: "other@schedule.example.test", ExpiresAt: time.Now().Add(time.Hour)}
	if w = request(http.MethodPost, repairPath, "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("cross tenant repair %d", w.Code)
	}
	if w = request(http.MethodDelete, path, "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("cross tenant cancel %d", w.Code)
	}
	if w = request(http.MethodGet, "/api/v1/mailbox/scheduled-sends", "", nil); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"items":[]`)) {
		t.Fatalf("cross tenant list %d %s", w.Code, w.Body.String())
	}
}

type sentRepairHTTPDialer struct{}
type sentRepairHTTPClient struct{ *fakeIMAPClient }

func (sentRepairHTTPDialer) Dial(context.Context, string, string) (webmail.Client, error) {
	return &sentRepairHTTPClient{fakeIMAPClient: &fakeIMAPClient{}}, nil
}
func (*sentRepairHTTPClient) FindSentCopy(context.Context, []byte) (webmail.DraftSaveResult, bool, error) {
	return webmail.DraftSaveResult{Folder: "Sent", UID: 77, UIDValidity: 9}, true, nil
}

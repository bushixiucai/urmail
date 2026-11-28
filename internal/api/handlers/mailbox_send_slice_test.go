package handlers

// Slice write-path tests (pm-webmail-writepath-1): the ledger-backed send
// pipeline, idempotent server-side drafts, receipts served from the SQLite
// ledger, tenant isolation and migration re-runnability.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type testSecretsBox struct{}

func (testSecretsBox) EncryptString(plain string) (string, error) {
	return "enc:v1:" + base64.StdEncoding.EncodeToString([]byte(plain)), nil
}

func (testSecretsBox) DecryptString(sealed string) (string, error) {
	if len(sealed) <= len("enc:v1:") || sealed[:len("enc:v1:")] != "enc:v1:" {
		return "", errors.New("not sealed")
	}
	raw, err := base64.StdEncoding.DecodeString(sealed[len("enc:v1:"):])
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// sentCopyClient adds Sent-copy reconciliation capability to the shared
// fake IMAP client for the slice send flow.
type sentCopyClient struct {
	*fakeIMAPClient
	savedCopies [][]byte
	reportFound bool
}

func (c *sentCopyClient) FindSentCopy(_ context.Context, raw []byte) (webmail.DraftSaveResult, bool, error) {
	_ = raw
	return webmail.DraftSaveResult{}, c.reportFound, nil
}

func (c *sentCopyClient) SaveSentCopy(_ context.Context, raw []byte) (webmail.DraftSaveResult, error) {
	c.savedCopies = append(c.savedCopies, raw)
	return webmail.DraftSaveResult{UID: 1}, nil
}

func (c *sentCopyClient) FolderUIDValidity(_ context.Context, _ string) (uint32, error) {
	return 9, nil
}

func (c *sentCopyClient) DeleteMessages(_ context.Context, _ string, _ []uint32, _ bool) error {
	return nil
}

func newSliceLedger(t *testing.T) *SQLiteSendLedger {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	handle, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err = sqlitedb.Migrate(ctx, handle, os.DirFS(filepath.Join("..", "..", "..")), "migrations_sqlite"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Seed the tenant identities so audit_logs FK targets exist.
	for _, stmt := range []string{
		`INSERT INTO brands(id,slug,name) VALUES(1,'ledger','Ledger')`,
		`INSERT INTO tenants(id,brand_id,name,slug,status) VALUES(7,1,'T7','t7','active'),(8,1,'T8','t8','active')`,
	} {
		if _, execErr := handle.ExecContext(ctx, stmt); execErr != nil {
			t.Fatalf("seed %q: %v", stmt, execErr)
		}
	}
	return &SQLiteSendLedger{DB: handle}
}

// sliceSend runs one authenticated request against the write-path handler.
func sliceSend(t *testing.T, h *MailboxNativeHandler, key, method, path string, body any) (int, string) {
	t.Helper()
	sessions := h.Sessions.(*fakeMailboxSessions)
	r := newNativeTestRouter(t, h, sessions)
	// The receipts routes live outside the shared harness (the receipts
	// integration test registers its own copies); same auth middleware.
	receipts := r.Group("/api/v1/mailbox")
	receipts.Use(middleware.RequireMailboxAuth(sessions))
	receipts.GET("/send-receipts", h.ListSendReceipts)
	receipts.GET("/send-receipts/:id", h.GetSendReceipt)
	receipts.POST("/send-receipts/:id/repair-sent-copy", h.RepairSendReceiptCopy)
	password := "correct-horse-battery"
	token := loginNative(t, r, "alice@example.test", password)
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), method, path, reader)
	req.Header.Set("Authorization", "Bearer "+token)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	r.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func newWritePathHandler(t *testing.T, ledger *SQLiteSendLedger, dialer *fakeDialer, submitter *fakeSubmitter) *MailboxNativeHandler {
	t.Helper()
	loginAttempts = newLoginLimiter()
	hash, hashErr := domainMail.HashPassword("correct-horse-battery")
	if hashErr != nil {
		t.Fatalf("hash: %v", hashErr)
	}
	return &MailboxNativeHandler{
		Enabled:    true,
		Auth:       fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: hash},
		Sessions:   &fakeMailboxSessions{},
		Vault:      webmail.NewMemoryVault(),
		Dialer:     dialer,
		Submitter:  submitter,
		Secrets:    testSecretsBox{},
		SendLedger: ledger,
	}
}

func TestSliceSendIdempotentExactlyOnce(t *testing.T) {
	ledger := newSliceLedger(t)
	submitter := &fakeSubmitter{}
	imap := &sentCopyClient{fakeIMAPClient: &fakeIMAPClient{}, reportFound: true}
	h := newWritePathHandler(t, ledger, &fakeDialer{wantAddress: "alice@example.test", client: imap}, submitter)
	send := func(key string, body map[string]any) (int, string) {
		return sliceSend(t, h, key, http.MethodPost, "/api/v1/mailbox/send", body)
	}

	body := map[string]any{"to": []string{"bob@example.test"}, "subject": "s1", "text": "hello"}
	code, out := send("key-one-0000000000", body)
	if code != http.StatusOK {
		t.Fatalf("send = %d %s", code, out)
	}
	if submitter.calls != 1 {
		t.Fatalf("submissions = %d, want exactly 1", submitter.calls)
	}
	row, err := ledger.GetByKey(context.Background(), 7, 9, "key-one-0000000000")
	if err != nil || row.Status != ledgerAccepted || row.SentCopyState != "complete" {
		t.Fatalf("ledger = %v copy=%v err=%v, want accepted+complete", row.Status, row.SentCopyState, err)
	}
	var auditCount int
	if err = ledger.DB.QueryRowContext(context.Background(),
		`SELECT count(*) FROM audit_logs WHERE action='mailbox.send.accepted' AND tenant_id=7`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("audit rows = %d err %v, want 1", auditCount, err)
	}

	// Same key + same content: replay, no second submission.
	code, _ = send("key-one-0000000000", body)
	if code != http.StatusOK || submitter.calls != 1 {
		t.Fatalf("replay = %d calls %d, want 200 and still 1 submission", code, submitter.calls)
	}
	// Same key + different content: conflict.
	code, _ = send("key-one-0000000000", map[string]any{"to": []string{"carol@example.test"}, "subject": "s1", "text": "hello"})
	if code != http.StatusConflict || submitter.calls != 1 {
		t.Fatalf("conflict = %d calls %d, want 409 and 1 submission", code, submitter.calls)
	}
	// A different key submits again.
	code, _ = send("key-two-0000000000", body)
	if code != http.StatusOK || submitter.calls != 2 {
		t.Fatalf("second key = %d calls %d, want 200 and 2 submissions", code, submitter.calls)
	}
	// The owner's receipt list serves exactly the ledger rows (2 here).
	_, out = sliceSend(t, h, "", http.MethodGet, "/api/v1/mailbox/send-receipts", nil)
	if got := strings.Count(out, `"request_key"`); got != 2 || strings.Contains(out, "foreign") {
		t.Fatalf("list rows = %d (%s), want 2 own rows", got, out)
	}
}

func TestSliceSendUncertainNeverAutoRetries(t *testing.T) {
	ledger := newSliceLedger(t)
	submitter := &fakeSubmitter{err: errors.New("smtp read timeout")}
	imap := &sentCopyClient{fakeIMAPClient: &fakeIMAPClient{}, reportFound: true}
	h := newWritePathHandler(t, ledger, &fakeDialer{wantAddress: "alice@example.test", client: imap}, submitter)
	send := func(key string) (int, string) {
		return sliceSend(t, h, key, http.MethodPost, "/api/v1/mailbox/send", map[string]any{"to": []string{"bob@example.test"}, "text": "hi"})
	}

	code, _ := send("uncertain-key-000000")
	if code != http.StatusBadGateway {
		t.Fatalf("uncertain send = %d, want 502", code)
	}
	row, err := ledger.GetByKey(context.Background(), 7, 9, "uncertain-key-000000")
	if err != nil || row.Status != ledgerUncertain {
		t.Fatalf("ledger = %v %v, want uncertain", row.Status, err)
	}
	// Replay returns the same receipt without a second submission.
	code, out := send("uncertain-key-000000")
	if code != http.StatusOK || submitter.calls != 1 || !strings.Contains(out, ledgerUncertain) {
		t.Fatalf("uncertain replay = %d calls %d out %s, want 200, 1 submission, uncertain row", code, submitter.calls, out)
	}

	// A hard SMTP rejection marks the row rejected; the key stays consumed.
	submitter2 := &fakeSubmitter{err: webmail.ErrSubmitFailed}
	h2 := newWritePathHandler(t, ledger, &fakeDialer{wantAddress: "alice@example.test", client: imap}, submitter2)
	code, _ = sliceSend(t, h2, "rejected-key-0000000", http.MethodPost, "/api/v1/mailbox/send", map[string]any{"to": []string{"bob@example.test"}, "text": "hi"})
	if code != http.StatusBadGateway {
		t.Fatalf("rejected send = %d, want 502", code)
	}
	row, err = ledger.GetByKey(context.Background(), 7, 9, "rejected-key-0000000")
	if err != nil || row.Status != ledgerRejected {
		t.Fatalf("ledger = %v %v, want rejected", row.Status, err)
	}
	if submitter2.calls != 1 {
		t.Fatalf("rejected submissions = %d, want 1", submitter2.calls)
	}
}

type countingDraftAppender struct {
	*fakeIMAPClient
	counter  *int
	savedRaw [][]byte
	nextUID  uint32
}

func (c *countingDraftAppender) AppendPreparedDraft(_ context.Context, folder string, raw []byte) (webmail.DraftSaveResult, error) {
	*c.counter++
	c.nextUID += 101
	c.savedRaw = append(c.savedRaw, raw)
	return webmail.DraftSaveResult{Folder: folder, UID: c.nextUID, UIDValidity: 9}, nil
}

func (c *countingDraftAppender) FindDraftCopy(_ context.Context, _ string, _ string, _ string) (webmail.DraftSaveResult, bool, error) {
	return webmail.DraftSaveResult{}, false, nil
}

func TestSliceDraftsIdempotentSave(t *testing.T) {
	ledger := newSliceLedger(t)
	appends := 0
	imap := &countingDraftAppender{
		fakeIMAPClient: &fakeIMAPClient{folders: []webmail.Folder{{Name: "Drafts", Delimiter: "/"}}},
		counter:        &appends,
		nextUID:        100,
	}
	h := newWritePathHandler(t, ledger, &fakeDialer{wantAddress: "alice@example.test", client: imap}, &fakeSubmitter{})
	draft := map[string]any{"to": []string{"bob@example.test"}, "subject": "d1", "text": "draft body"}

	code, out := sliceSend(t, h, "draft-key-000000000", http.MethodPut, "/api/v1/mailbox/drafts", draft)
	if code != http.StatusOK {
		t.Fatalf("save = %d %s", code, out)
	}
	var saved struct {
		UID   uint32 `json:"uid"`
		Saved bool   `json:"saved"`
	}
	if err := json.Unmarshal([]byte(out), &saved); err != nil || !saved.Saved {
		t.Fatalf("save payload %s (%v)", out, err)
	}
	if saved.UID == 0 || appends != 1 {
		t.Fatalf("uid=%d appends=%d out=%s, want appended once", saved.UID, appends, out)
	}
	// Replay: same key, same content — the journaled result, no second append.
	code, out = sliceSend(t, h, "draft-key-000000000", http.MethodPut, "/api/v1/mailbox/drafts", draft)
	if code != http.StatusOK || appends != 1 {
		t.Fatalf("replay = %d appends %d (%s), want 200 and 1 append", code, appends, out)
	}
	// Same key, different content: conflict.
	code, _ = sliceSend(t, h, "draft-key-000000000", http.MethodPut, "/api/v1/mailbox/drafts", map[string]any{"to": []string{"carol@example.test"}, "text": "other"})
	if code != http.StatusConflict || appends != 1 {
		t.Fatalf("draft conflict = %d appends %d, want 409 and 1 append", code, appends)
	}
	// The journal row is done with the journaled result.
	var status, resultJSON string
	if scanErr := ledger.DB.QueryRowContext(context.Background(),
		`SELECT "status","result_json" FROM mailbox_draft_journal WHERE request_key='draft-key-000000000'`).Scan(&status, &resultJSON); scanErr != nil || status != "done" {
		t.Fatalf("journal = %s %v, want done", status, scanErr)
	}
	if !strings.Contains(resultJSON, `"uid":`) {
		t.Fatalf("journal result json missing uid: %s", resultJSON)
	}
}

func TestSliceReceiptsIsolationAndLedgerList(t *testing.T) {
	ledger := newSliceLedger(t)
	// Seed a foreign-tenant receipt directly (tenant 8, mailbox 12).
	if _, err := ledger.DB.ExecContext(context.Background(),
		`INSERT INTO mailbox_send_ledger("tenant_id","mailbox_id","request_key","request_hash","status")
		 VALUES(8,12,'foreign-key','hash-a','accepted')`); err != nil {
		t.Fatal(err)
	}
	var foreignID int64
	if err := ledger.DB.QueryRowContext(context.Background(),
		`SELECT id FROM mailbox_send_ledger WHERE request_key='foreign-key'`).Scan(&foreignID); err != nil {
		t.Fatal(err)
	}
	submitter := &fakeSubmitter{}
	imap := &sentCopyClient{fakeIMAPClient: &fakeIMAPClient{}, reportFound: true}
	h := newWritePathHandler(t, ledger, &fakeDialer{wantAddress: "alice@example.test", client: imap}, submitter)

	code, out := sliceSend(t, h, "alice-key-000000000", http.MethodPost, "/api/v1/mailbox/send", map[string]any{"to": []string{"bob@example.test"}, "text": "hi"})
	if code != http.StatusOK {
		t.Fatalf("send = %d %s", code, out)
	}
	code, out = sliceSend(t, h, "", http.MethodGet, "/api/v1/mailbox/send-receipts", nil)
	if code != http.StatusOK {
		t.Fatalf("list = %d %s", code, out)
	}
	if strings.Contains(out, "foreign-key") {
		t.Fatalf("receipt list leaked a foreign-tenant row: %s", out)
	}
	// A foreign-tenant receipt id is not addressable by the owner.
	code, out = sliceSend(t, h, "", http.MethodGet, "/api/v1/mailbox/send-receipts/"+int64String(foreignID), nil)
	if code != http.StatusNotFound {
		t.Fatalf("foreign receipt get = %d %s, want 404", code, out)
	}
	// The owner's own receipt is addressable.
	var ownID int64
	if err := ledger.DB.QueryRowContext(context.Background(),
		`SELECT id FROM mailbox_send_ledger WHERE request_key='alice-key-000000000'`).Scan(&ownID); err != nil {
		t.Fatal(err)
	}
	code, _ = sliceSend(t, h, "", http.MethodGet, "/api/v1/mailbox/send-receipts/"+int64String(ownID), nil)
	if code != http.StatusOK {
		t.Fatalf("own receipt get = %d, want 200", code)
	}
}

func TestSendLedgerMigrationReRunnable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mig.db")
	handle, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	if err = sqlitedb.Migrate(ctx, handle, os.DirFS(filepath.Join("..", "..", "..")), "migrations_sqlite"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var version int
	if err = handle.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != sqlitedb.ExpectedSchemaVersion {
		t.Fatalf("version=%d err=%v want %d", version, err, sqlitedb.ExpectedSchemaVersion)
	}
	// Re-run the migrator: a no-op at the tip, no error, no schema change.
	if err = sqlitedb.Migrate(ctx, handle, os.DirFS(filepath.Join("..", "..", "..")), "migrations_sqlite"); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	var tableCount int
	if err = handle.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('mailbox_send_ledger','mailbox_draft_journal')`).Scan(&tableCount); err != nil || tableCount != 2 {
		t.Fatalf("ledger tables=%d err=%v, want 2", tableCount, err)
	}
}

func int64String(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type fakeMailboxAuth struct {
	tenantID, mailboxID int64
	address, hash       string
	err                 error
}

func (f fakeMailboxAuth) Lookup(_ context.Context, address string) (int64, int64, string, string, error) {
	if f.err != nil {
		return 0, 0, "", "", f.err
	}
	if address != f.address {
		return 0, 0, "", "", pgx.ErrNoRows
	}
	return f.tenantID, f.mailboxID, f.address, f.hash, nil
}

type fakeMailboxSessions struct {
	byHash map[string]webmail.Session
}

func (f *fakeMailboxSessions) Create(_ context.Context, tokenHash, address string) (webmail.Session, error) {
	if f.byHash == nil {
		f.byHash = make(map[string]webmail.Session)
	}
	session := webmail.Session{
		TokenHash: tokenHash,
		TenantID:  7,
		MailboxID: 9,
		Address:   address,
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}
	f.byHash[tokenHash] = session
	return session, nil
}

func (f *fakeMailboxSessions) Get(_ context.Context, tokenHash string) (webmail.Session, error) {
	session, ok := f.byHash[tokenHash]
	if !ok {
		return webmail.Session{}, pgx.ErrNoRows
	}
	return session, nil
}

func (f *fakeMailboxSessions) Revoke(_ context.Context, tokenHash string) error {
	delete(f.byHash, tokenHash)
	return nil
}

func (f *fakeMailboxSessions) GetMailboxSession(ctx context.Context, tokenHash string) (webmail.Session, error) {
	return f.Get(ctx, tokenHash)
}

type fakeIMAPClient struct {
	folders  []webmail.Folder
	listed   webmail.ListResult
	message  *webmail.Message
	flagged  []uint32
	moved    []uint32
	deleted  []uint32
	moveTo   string
	expunged bool
}

func (f *fakeIMAPClient) ListFolders(context.Context) ([]webmail.Folder, error) {
	return webmail.WithEncodedNames(f.folders), nil
}
func (f *fakeIMAPClient) ListMessages(context.Context, string, webmail.ListOpts) (webmail.ListResult, error) {
	return f.listed, nil
}
func (f *fakeIMAPClient) GetMessage(_ context.Context, _ string, uid uint32) (*webmail.Message, error) {
	if f.message == nil || f.message.UID != uid {
		return nil, fmt.Errorf("message not found")
	}
	return f.message, nil
}
func (f *fakeIMAPClient) SetFlags(_ context.Context, _ string, uids []uint32, _, _ []string) error {
	f.flagged = append([]uint32{}, uids...)
	return nil
}
func (f *fakeIMAPClient) MoveMessages(_ context.Context, _ string, uids []uint32, dest string) error {
	f.moved = append([]uint32{}, uids...)
	f.moveTo = dest
	return nil
}
func (f *fakeIMAPClient) DeleteMessages(_ context.Context, _ string, uids []uint32, expunge bool) error {
	f.deleted = append([]uint32{}, uids...)
	f.expunged = expunge
	return nil
}
func (f *fakeIMAPClient) Close() error { return nil }

type fakeDialer struct {
	wantAddress string
	client      webmail.Client
	err         error
	dialed      bool
}

func (d *fakeDialer) Dial(_ context.Context, address, password string) (webmail.Client, error) {
	d.dialed = true
	if d.err != nil {
		return nil, d.err
	}
	if address != d.wantAddress || password == "" {
		return nil, errors.New("bad credentials")
	}
	return d.client, nil
}

type fakeSubmitter struct {
	last webmail.Outgoing
	err  error
}

func (s *fakeSubmitter) Send(_ context.Context, address, password string, msg webmail.Outgoing) error {
	if s.err != nil {
		return s.err
	}
	if address == "" || password == "" {
		return errors.New("missing auth")
	}
	s.last = msg
	return nil
}

func newNativeTestRouter(t *testing.T, h *MailboxNativeHandler, sessions middleware.MailboxSessionLookup) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.GET("/api/v1/mailbox/status", h.Status)
	r.POST("/api/v1/mailbox/session", h.CreateSession)
	auth := r.Group("/api/v1/mailbox")
	auth.Use(middleware.RequireMailboxAuth(sessions))
	auth.GET("/folders", h.ListFolders)
	auth.GET("/folders/:name/messages", h.ListMessages)
	auth.POST("/folders/:name/messages/flags", h.FlagMessages)
	auth.POST("/folders/:name/messages/move", h.MoveMessages)
	auth.POST("/folders/:name/messages/delete", h.DeleteMessages)
	auth.GET("/folders/:name/messages/:uid", h.GetMessage)
	auth.POST("/send", h.SendMessage)
	auth.POST("/password", h.ChangePassword)
	auth.DELETE("/session", h.DeleteSession)
	return r
}

func loginNative(t *testing.T, r *gin.Engine, email, password string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mailbox/session", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create session status %d body %s", w.Code, w.Body.String())
	}
	var created struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Token == "" {
		t.Fatalf("decode session: %v body %s", err, w.Body.String())
	}
	return created.Token
}

func TestMailboxNativeCreateSessionAndListFolders(t *testing.T) {
	loginAttempts = newLoginLimiter()
	password := "correct-horse-battery"
	hash, err := domainMail.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	sessions := &fakeMailboxSessions{}
	vault := webmail.NewMemoryVault()
	dialer := &fakeDialer{
		wantAddress: "alice@example.test",
		client: &fakeIMAPClient{folders: []webmail.Folder{
			{Name: "INBOX", Delimiter: "/", Flags: []string{`\HasNoChildren`}},
		}},
	}
	h := &MailboxNativeHandler{
		Enabled:  true,
		Auth:     fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: hash},
		Sessions: sessions,
		Vault:    vault,
		Dialer:   dialer,
	}
	r := newNativeTestRouter(t, h, sessions)
	token := loginNative(t, r, "Alice@Example.test", password)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mailbox/folders", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("folders status %d body %s", w.Code, w.Body.String())
	}
	if !dialer.dialed {
		t.Fatal("expected IMAP dial")
	}
	var listed struct {
		Folders []webmail.Folder `json:"folders"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode folders: %v", err)
	}
	if len(listed.Folders) != 1 || listed.Folders[0].Name != "INBOX" || listed.Folders[0].NameEnc == "" {
		t.Fatalf("folders = %#v", listed.Folders)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/mailbox/session", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("logout status %d", w.Code)
	}
	if _, ok := vault.Get(mailboxprincipal.HashToken(token)); ok {
		t.Fatal("vault should clear password on logout")
	}
}

func TestMailboxNativeMessagesFlagsMoveDeleteSend(t *testing.T) {
	loginAttempts = newLoginLimiter()
	password := "correct-horse-battery"
	hash, err := domainMail.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	sessions := &fakeMailboxSessions{}
	imap := &fakeIMAPClient{
		listed: webmail.ListResult{
			Total: 1,
			Messages: []webmail.MessageSummary{{
				UID: 7, Subject: "Hello", From: []webmail.Address{{Email: "bob@example.test"}}, Size: 12,
			}},
		},
		message: &webmail.Message{
			MessageSummary: webmail.MessageSummary{UID: 7, Subject: "Hello"},
			Text:           "plain",
			HTML:           "<p>plain</p>",
			Attachments:    []webmail.AttachmentMeta{{Filename: "a.txt", ContentType: "text/plain", Size: 1}},
		},
	}
	submit := &fakeSubmitter{}
	h := &MailboxNativeHandler{
		Enabled:   true,
		Auth:      fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: hash},
		Sessions:  sessions,
		Vault:     webmail.NewMemoryVault(),
		Dialer:    &fakeDialer{wantAddress: "alice@example.test", client: imap},
		Submitter: submit,
	}
	r := newNativeTestRouter(t, h, sessions)
	token := loginNative(t, r, "alice@example.test", password)
	folder := webmail.EncodeFolderName("INBOX")
	auth := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var reader *bytes.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		} else {
			reader = bytes.NewReader(nil)
		}
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		r.ServeHTTP(w, req)
		return w
	}

	w := auth(http.MethodGet, "/api/v1/mailbox/folders/"+folder+"/messages?limit=10", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list messages %d %s", w.Code, w.Body.String())
	}
	w = auth(http.MethodGet, "/api/v1/mailbox/folders/"+folder+"/messages/7", nil)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"plain"`)) {
		t.Fatalf("get message %d %s", w.Code, w.Body.String())
	}
	w = auth(http.MethodPost, "/api/v1/mailbox/folders/"+folder+"/messages/flags", map[string]any{
		"uids": []int{7}, "add": []string{`\Seen`},
	})
	if w.Code != http.StatusOK || len(imap.flagged) != 1 {
		t.Fatalf("flags %d %#v", w.Code, imap.flagged)
	}
	w = auth(http.MethodPost, "/api/v1/mailbox/folders/"+folder+"/messages/move", map[string]any{
		"uids": []int{7}, "to": webmail.EncodeFolderName("Archive"),
	})
	if w.Code != http.StatusOK || imap.moveTo != "Archive" {
		t.Fatalf("move %d to=%q", w.Code, imap.moveTo)
	}
	w = auth(http.MethodPost, "/api/v1/mailbox/folders/"+folder+"/messages/delete", map[string]any{
		"uids": []int{7}, "expunge": true,
	})
	if w.Code != http.StatusOK || !imap.expunged {
		t.Fatalf("delete %d expunge=%v", w.Code, imap.expunged)
	}
	w = auth(http.MethodPost, "/api/v1/mailbox/send", map[string]any{
		"to": []string{"bob@example.test"}, "subject": "Hi", "text": "body",
	})
	if w.Code != http.StatusOK || submit.last.Subject != "Hi" {
		t.Fatalf("send %d %#v", w.Code, submit.last)
	}
}

func TestMailboxNativeRejectsBadPassword(t *testing.T) {
	loginAttempts = newLoginLimiter()
	hash, err := domainMail.HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	sessions := &fakeMailboxSessions{}
	h := &MailboxNativeHandler{
		Enabled:  true,
		Auth:     fakeMailboxAuth{tenantID: 1, mailboxID: 2, address: "alice@example.test", hash: hash},
		Sessions: sessions,
		Vault:    webmail.NewMemoryVault(),
		Dialer:   &fakeDialer{},
	}
	r := newNativeTestRouter(t, h, sessions)
	body, _ := json.Marshal(map[string]string{"email": "alice@example.test", "password": "wrong"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mailbox/session", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", w.Code)
	}
}

func TestMailboxNativeDisabled(t *testing.T) {
	h := &MailboxNativeHandler{Enabled: false}
	r := newNativeTestRouter(t, h, &fakeMailboxSessions{})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/mailbox/status", nil))
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"native":false`)) {
		t.Fatalf("status = %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mailbox/session", bytes.NewReader([]byte(`{"email":"a@b.c","password":"x"}`)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("create while disabled = %d", w.Code)
	}
}

func TestMailboxNativeFoldersRequireBearer(t *testing.T) {
	sessions := &fakeMailboxSessions{}
	h := &MailboxNativeHandler{
		Enabled:  true,
		Auth:     fakeMailboxAuth{},
		Sessions: sessions,
		Vault:    webmail.NewMemoryVault(),
		Dialer:   &fakeDialer{},
	}
	r := newNativeTestRouter(t, h, sessions)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/mailbox/folders", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", w.Code)
	}
}

func TestMailboxNativePasswordRequiresPool(t *testing.T) {
	loginAttempts = newLoginLimiter()
	password := "correct-horse-battery"
	hash, err := domainMail.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	sessions := &fakeMailboxSessions{}
	h := &MailboxNativeHandler{
		Enabled:  true,
		Auth:     fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: hash},
		Sessions: sessions,
		Vault:    webmail.NewMemoryVault(),
		Dialer:   &fakeDialer{wantAddress: "alice@example.test", client: &fakeIMAPClient{}},
	}
	r := newNativeTestRouter(t, h, sessions)
	token := loginNative(t, r, "alice@example.test", password)
	body, _ := json.Marshal(map[string]string{"old_password": password, "new_password": "new-secret-password"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mailbox/password", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("password without pool = %d %s", w.Code, w.Body.String())
	}
}

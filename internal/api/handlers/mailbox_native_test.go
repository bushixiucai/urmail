package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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

func (f *fakeMailboxSessions) Create(_ context.Context, tokenHash, address, _ string, _ bool) (webmail.Session, error) {
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
	folders        []webmail.Folder
	listed         webmail.ListResult
	message        *webmail.Message
	flagged        []uint32
	moved          []uint32
	deleted        []uint32
	moveTo         string
	expunged       bool
	lastListOpts   webmail.ListOpts
	draftFolder    string
	savedDraft     webmail.Outgoing
	replaceUID     uint32
	nextDraftUID   uint32
	attachmentErr  error
	draftErr       error
	cleanupPending bool
}

func (f *fakeIMAPClient) ListFolders(context.Context) ([]webmail.Folder, error) {
	return webmail.WithEncodedNames(f.folders), nil
}
func (f *fakeIMAPClient) ListMessages(_ context.Context, _ string, opts webmail.ListOpts) (webmail.ListResult, error) {
	f.lastListOpts = opts
	return f.listed, nil
}
func (f *fakeIMAPClient) GetMessage(_ context.Context, _ string, uid uint32) (*webmail.Message, error) {
	if f.message == nil || f.message.UID != uid {
		return nil, fmt.Errorf("message not found")
	}
	return f.message, nil
}
func (f *fakeIMAPClient) GetAttachment(_ context.Context, _ string, uid uint32, partID string) (string, string, []byte, error) {
	if f.attachmentErr != nil {
		return "", "", nil, f.attachmentErr
	}
	if f.message == nil || f.message.UID != uid {
		return "", "", nil, fmt.Errorf("message not found")
	}
	for _, a := range f.message.Attachments {
		if a.PartID == partID {
			return a.Filename, a.ContentType, []byte("attachment-bytes"), nil
		}
	}
	return "", "", nil, fmt.Errorf("attachment not found")
}
func (f *fakeIMAPClient) SetFlags(_ context.Context, _ string, uids []uint32, _, _ []string) error {
	f.flagged = append([]uint32{}, uids...)
	return nil
}
func (f *fakeIMAPClient) MoveMessages(_ context.Context, _ string, uids []uint32, dest string) (webmail.MoveResult, error) {
	f.moved = append([]uint32{}, uids...)
	f.moveTo = dest
	destinations := make([]uint32, 0, len(uids))
	for _, uid := range uids {
		destinations = append(destinations, uid+1000)
	}
	return webmail.MoveResult{DestinationUIDs: destinations, DestinationUIDValidity: 42}, nil
}
func (f *fakeIMAPClient) DeleteMessages(_ context.Context, _ string, uids []uint32, expunge bool) error {
	f.deleted = append([]uint32{}, uids...)
	f.expunged = expunge
	return nil
}
func (f *fakeIMAPClient) EnsureDraftsFolder(context.Context) (string, error) {
	if name := webmail.ResolveDraftsFolder(f.folders); name != "" {
		f.draftFolder = name
		return name, nil
	}
	f.draftFolder = webmail.DraftsFolderName
	f.folders = append(f.folders, webmail.Folder{
		Name: webmail.DraftsFolderName, Delimiter: "/", Flags: []string{`\Drafts`},
	})
	return f.draftFolder, nil
}
func (f *fakeIMAPClient) FolderUIDValidity(context.Context, string) (uint32, error) {
	return 9, nil
}

func (f *fakeIMAPClient) SaveDraft(_ context.Context, _ string, msg webmail.Outgoing, replaceUID, replaceUIDValidity uint32) (webmail.DraftSaveResult, error) {
	if f.draftErr != nil {
		return webmail.DraftSaveResult{}, f.draftErr
	}
	if replaceUID != 0 && replaceUIDValidity != 9 {
		return webmail.DraftSaveResult{}, webmail.ErrDraftChanged
	}
	folder, err := f.EnsureDraftsFolder(context.Background())
	if err != nil {
		return webmail.DraftSaveResult{}, err
	}
	f.savedDraft = msg
	f.replaceUID = replaceUID
	if replaceUID != 0 {
		f.deleted = append([]uint32{}, replaceUID)
		f.expunged = true
	}
	uid := f.nextDraftUID
	if uid == 0 {
		uid = 101
	}
	f.nextDraftUID = uid + 1
	return webmail.DraftSaveResult{
		Folder:         folder,
		NameEnc:        webmail.EncodeFolderName(folder),
		UID:            uid,
		UIDValidity:    9,
		CleanupPending: f.cleanupPending,
	}, nil
}
func (f *fakeIMAPClient) SaveSentCopy(context.Context, []byte) (webmail.DraftSaveResult, error) {
	return webmail.DraftSaveResult{Folder: "Sent", UID: 1, UIDValidity: 1}, nil
}
func (f *fakeIMAPClient) MoveToTrash(_ context.Context, _ string, uids []uint32) (string, error) {
	f.moveTo = "Trash"
	f.moved = append([]uint32{}, uids...)
	return "Trash", nil
}
func (f *fakeIMAPClient) Close() error { return nil }
func (f *fakeIMAPClient) GetThreadMembers(context.Context, string, uint32, webmail.ThreadMembersOpts) (webmail.ThreadMembersResult, error) {
	return webmail.ThreadMembersResult{}, nil
}

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
	last  webmail.Outgoing
	err   error
	calls int
}

func (s *fakeSubmitter) Send(_ context.Context, address, password string, msg webmail.Outgoing) (webmail.Submission, error) {
	s.calls++
	if s.err != nil {
		return webmail.Submission{}, s.err
	}
	if address == "" || password == "" {
		return webmail.Submission{}, errors.New("missing auth")
	}
	s.last = msg
	return webmail.Submission{Raw: []byte("From: test\r\n\r\nbody")}, nil
}

func newNativeTestRouter(t *testing.T, h *MailboxNativeHandler, sessions middleware.MailboxSessionLookup) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(middleware.BodyLimitWithMailboxUploads(1<<20, 16<<20))
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
	auth.GET("/folders/:name/messages/:uid/attachments/:part_id", h.GetAttachment)
	auth.GET("/folders/:name/messages/:uid/raw", h.GetRawMessage)
	auth.POST("/folders/:name/import", h.ImportMessage)
	auth.POST("/send", h.SendMessage)
	auth.PUT("/drafts", h.SaveDraft)
	auth.GET("/drafts", h.ListDrafts)
	auth.DELETE("/drafts/:uid", h.DeleteDraft)
	auth.POST("/password", h.ChangePassword)
	auth.DELETE("/session", h.DeleteSession)
	return r
}

func loginNative(t *testing.T, r *gin.Engine, email, password string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/mailbox/session", bytes.NewReader(body))
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/folders", nil)
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
	req = httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/api/v1/mailbox/session", nil)
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
			Attachments:    []webmail.AttachmentMeta{{PartID: "3", Filename: "a.txt", ContentType: "text/plain", Size: 1}},
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
		req := httptest.NewRequestWithContext(context.Background(), method, path, reader)
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
	w = auth(http.MethodGet, "/api/v1/mailbox/folders/"+folder+"/messages/7/attachments/3", nil)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), []byte("attachment-bytes")) {
		t.Fatalf("get attachment %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), "a.txt") {
		t.Fatalf("disposition=%q", w.Header().Get("Content-Disposition"))
	}
	for _, sample := range []struct {
		value  string
		status int
	}{{"0", 400}, {"bad", 400}, {"8", 409}, {"9", 200}} {
		checked := auth(http.MethodGet, "/api/v1/mailbox/folders/"+folder+"/messages/7/attachments/3?uid_validity="+sample.value, nil)
		if checked.Code != sample.status {
			t.Fatalf("attachment generation %s: %d %s", sample.value, checked.Code, checked.Body.String())
		}
		if sample.status != 200 && bytes.Contains(checked.Body.Bytes(), []byte("attachment-bytes")) {
			t.Fatal("stale generation leaked attachment bytes")
		}
	}
	for _, action := range []string{"flags", "move", "delete"} {
		for _, validity := range []uint32{0, 8} {
			rejected := auth(http.MethodPost, "/api/v1/mailbox/folders/"+folder+"/messages/"+action, map[string]any{
				"uids": []int{7}, "add": []string{`\Seen`}, "to": "Archive", "expunge": true, "uid_validity": validity,
			})
			want := http.StatusBadRequest
			if validity != 0 {
				want = http.StatusConflict
			}
			if rejected.Code != want {
				t.Fatalf("%s validity %d: %d %s", action, validity, rejected.Code, rejected.Body.String())
			}
		}
	}
	if len(imap.moved) != 0 {
		t.Fatalf("invalid UIDVALIDITY must not move messages: %v", imap.moved)
	}
	w = auth(http.MethodPost, "/api/v1/mailbox/folders/"+folder+"/messages/flags", map[string]any{
		"uid_validity": 9, "uids": []int{7}, "add": []string{`\Seen`},
	})
	if w.Code != http.StatusOK || len(imap.flagged) != 1 {
		t.Fatalf("flags %d %#v", w.Code, imap.flagged)
	}
	w = auth(http.MethodPost, "/api/v1/mailbox/folders/"+folder+"/messages/move", map[string]any{
		"uid_validity": 9, "uids": []int{7}, "to": webmail.EncodeFolderName("Archive"),
	})
	if w.Code != http.StatusOK || imap.moveTo != "Archive" {
		t.Fatalf("move %d to=%q", w.Code, imap.moveTo)
	}
	var moveResp struct {
		Moved                  int      `json:"moved"`
		To                     string   `json:"to"`
		DestinationUIDs        []uint32 `json:"destination_uids"`
		DestinationUIDValidity uint32   `json:"destination_uid_validity"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &moveResp); err != nil {
		t.Fatalf("move response %v: %s", err, w.Body.String())
	}
	if moveResp.Moved != 1 || moveResp.DestinationUIDValidity != 42 || !slices.Equal(moveResp.DestinationUIDs, []uint32{1007}) {
		t.Fatalf("move response %#v", moveResp)
	}
	// Move targets must be managed folders and differ from the source
	// (same guard as transferMessages).
	// " trailing-space" is intentionally absent: move trims the destination
	// before validating (pre-existing leniency over transferMessages).
	for _, badDest := range []string{"../escape", "INBOX"} {
		rejected := auth(http.MethodPost, "/api/v1/mailbox/folders/"+folder+"/messages/move", map[string]any{
			"uid_validity": 9, "uids": []int{7}, "to": badDest,
		})
		if rejected.Code != http.StatusBadRequest {
			t.Fatalf("move to %q: %d %s", badDest, rejected.Code, rejected.Body.String())
		}
	}
	w = auth(http.MethodPost, "/api/v1/mailbox/folders/"+folder+"/messages/delete", map[string]any{
		"uid_validity": 9, "uids": []int{7}, "expunge": true,
	})
	if w.Code != http.StatusOK || !imap.expunged {
		t.Fatalf("delete %d expunge=%v", w.Code, imap.expunged)
	}
	// Without durable receipt storage a send must fail before SMTP.
	w = auth(http.MethodPost, "/api/v1/mailbox/send", map[string]any{"to": []string{"bob@example.test"}, "text": "body"})
	if w.Code != http.StatusServiceUnavailable || submit.calls != 0 {
		t.Fatalf("send without durable store: status=%d calls=%d", w.Code, submit.calls)
	}
	for _, failure := range []struct {
		err    error
		status int
		code   string
	}{
		{webmail.ErrAttachmentTooLarge, http.StatusRequestEntityTooLarge, "ATTACHMENT_TOO_LARGE"},
		{webmail.ErrInvalidMIME, http.StatusUnprocessableEntity, "ATTACHMENT_INVALID"},
	} {
		imap.attachmentErr = failure.err
		w = auth(http.MethodGet, "/api/v1/mailbox/folders/"+folder+"/messages/7/attachments/3", nil)
		if w.Code != failure.status || !bytes.Contains(w.Body.Bytes(), []byte(failure.code)) || strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
			t.Fatalf("attachment failure %d %s", w.Code, w.Body.String())
		}
	}
}

func TestMailboxNativeDraftsRequireDurableJournal(t *testing.T) {
	loginAttempts = newLoginLimiter()
	password := "correct-horse-battery"
	hash, err := domainMail.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	sessions := &fakeMailboxSessions{}
	imap := &fakeIMAPClient{
		folders: []webmail.Folder{{Name: "INBOX", Delimiter: "/"}},
		listed: webmail.ListResult{
			Total: 1,
			Messages: []webmail.MessageSummary{{
				UID: 101, Subject: "Draft hi", Flags: []string{`\Draft`},
			}},
		},
	}
	h := &MailboxNativeHandler{
		Enabled:  true,
		Auth:     fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: hash},
		Sessions: sessions,
		Vault:    webmail.NewMemoryVault(),
		Dialer:   &fakeDialer{wantAddress: "alice@example.test", client: imap},
	}
	r := newNativeTestRouter(t, h, sessions)
	token := loginNative(t, r, "alice@example.test", password)
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
		req := httptest.NewRequestWithContext(context.Background(), method, path, reader)
		req.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		r.ServeHTTP(w, req)
		return w
	}

	w := auth(http.MethodPut, "/api/v1/mailbox/drafts", map[string]any{"to": []string{"bob@example.test"}, "text": "body"})
	if w.Code != http.StatusBadRequest || imap.savedDraft.Text != "" {
		t.Fatalf("missing save identity must not append: %d %s", w.Code, w.Body.String())
	}
	w = auth(http.MethodGet, "/api/v1/mailbox/drafts?limit=10", nil)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"Draft hi"`)) {
		t.Fatalf("list %d %s", w.Code, w.Body.String())
	}
	w = auth(http.MethodDelete, "/api/v1/mailbox/drafts/101?uid_validity=9", nil)
	if w.Code != http.StatusServiceUnavailable || len(imap.deleted) != 0 {
		t.Fatalf("missing journal must not delete: %d %s", w.Code, w.Body.String())
	}
}

func TestMailboxNativeListMessagesSearchQuery(t *testing.T) {
	loginAttempts = newLoginLimiter()
	password := "correct-horse-battery"
	hash, err := domainMail.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	sessions := &fakeMailboxSessions{}
	imap := &fakeIMAPClient{
		folders: []webmail.Folder{{Name: "INBOX", Delimiter: "/"}},
		listed: webmail.ListResult{
			Messages: []webmail.MessageSummary{{UID: 3, Subject: "hit"}},
			Total:    1,
		},
	}
	h := &MailboxNativeHandler{
		Enabled:  true,
		Auth:     fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: hash},
		Sessions: sessions,
		Vault:    webmail.NewMemoryVault(),
		Dialer:   &fakeDialer{wantAddress: "alice@example.test", client: imap},
	}
	r := newNativeTestRouter(t, h, sessions)
	token := loginNative(t, r, "alice@example.test", password)
	folder := webmail.EncodeFolderName("INBOX")

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/folders/"+folder+"/messages?q=from:bob+is:unseen&limit=10", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list search %d %s", w.Code, w.Body.String())
	}
	if imap.lastListOpts.Query != "from:bob is:unseen" {
		t.Fatalf("query=%q", imap.lastListOpts.Query)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"q":"from:bob is:unseen"`)) {
		t.Fatalf("body=%s", w.Body.String())
	}

	bad := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/folders/"+folder+"/messages?q=before:1", nil)
	bad.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, bad)
	if w.Code != http.StatusBadRequest || !bytes.Contains(w.Body.Bytes(), []byte("INVALID_SEARCH_QUERY")) {
		t.Fatalf("bad q %d %s", w.Code, w.Body.String())
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/mailbox/session", bytes.NewReader(body))
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
	r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/status", nil))
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"native":false`)) {
		t.Fatalf("status = %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/mailbox/session", bytes.NewReader([]byte(`{"email":"a@b.c","password":"x"}`)))
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
	r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/folders", nil))
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/mailbox/password", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("password without pool = %d %s", w.Code, w.Body.String())
	}
}

type rawCapableClient struct {
	fakeIMAPClient
	raw       []byte
	rawErr    error
	imported  [][]byte
	importErr error
}

func (c *rawCapableClient) GetRawMessage(_ context.Context, _ string, uid uint32) ([]byte, uint32, error) {
	if c.rawErr != nil {
		return nil, 0, c.rawErr
	}
	if uid == 0 {
		return nil, 0, webmail.ErrMessageNotFound
	}
	return c.raw, 9, nil
}

func (c *rawCapableClient) ImportMessage(_ context.Context, _ string, raw []byte) (webmail.DraftSaveResult, error) {
	if c.importErr != nil {
		return webmail.DraftSaveResult{}, c.importErr
	}
	c.imported = append(c.imported, raw)
	return webmail.DraftSaveResult{Folder: "INBOX", UID: 77, UIDValidity: 9}, nil
}

func TestGetRawMessageStreamsExactPayload(t *testing.T) {
	payload := "From: a@example.test\r\n\r\nhello\r\n"
	client := &rawCapableClient{raw: []byte(payload)}
	sessions := &fakeMailboxSessions{}
	authHash, hashErr := domainMail.HashPassword("correct-horse-battery")
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	h := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: authHash}, Sessions: sessions, Vault: webmail.NewMemoryVault(), Submitter: &fakeSubmitter{}}
	h.Dialer = &fakeDialer{wantAddress: "alice@example.test", client: client}
	r := newNativeTestRouter(t, h, sessions)
	token := loginNative(t, r, "alice@example.test", "correct-horse-battery")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/folders/INBOX/messages/5/raw", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != payload {
		t.Fatalf("%d %q", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "message/rfc822" {
		t.Fatalf("content type %q", ct)
	}
	for _, sample := range []struct {
		value  string
		status int
	}{{"0", 400}, {"8", 409}, {"9", 200}} {
		read := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/mailbox/folders/INBOX/messages/5/raw?uid_validity="+sample.value, nil)
		read.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		r.ServeHTTP(response, read)
		if response.Code != sample.status {
			t.Fatalf("raw generation %s: %d", sample.value, response.Code)
		}
		if sample.status != 200 && strings.Contains(response.Body.String(), "hello") {
			t.Fatal("stale generation leaked raw message")
		}
	}
}

func TestGetRawMessageNotFound(t *testing.T) {
	client := &rawCapableClient{rawErr: webmail.ErrMessageNotFound}
	sessions := &fakeMailboxSessions{}
	authHash, hashErr := domainMail.HashPassword("correct-horse-battery")
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	h := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: authHash}, Sessions: sessions, Vault: webmail.NewMemoryVault(), Submitter: &fakeSubmitter{}}
	h.Dialer = &fakeDialer{wantAddress: "alice@example.test", client: client}
	r := newNativeTestRouter(t, h, sessions)
	token := loginNative(t, r, "alice@example.test", "correct-horse-battery")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/folders/INBOX/messages/99/raw", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestImportMessageRequiresReviewedReceipt(t *testing.T) {
	client := &rawCapableClient{}
	sessions := &fakeMailboxSessions{}
	authHash, hashErr := domainMail.HashPassword("correct-horse-battery")
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	h := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: authHash}, Sessions: sessions, Vault: webmail.NewMemoryVault(), Submitter: &fakeSubmitter{}}
	h.Dialer = &fakeDialer{wantAddress: "alice@example.test", client: client}
	r := newNativeTestRouter(t, h, sessions)
	token := loginNative(t, r, "alice@example.test", "correct-horse-battery")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/mailbox/folders/INBOX/import", strings.NewReader("From: x@y\r\n\r\nhi\r\n"))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "message/rfc822")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict || len(client.imported) != 0 || !strings.Contains(w.Body.String(), "MAIL_IMPORT_PREVIEW_REQUIRED") {
		t.Fatalf("%d %s imported=%d", w.Code, w.Body.String(), len(client.imported))
	}
}

func TestLegacyImportCannotBypassReviewWithDifferentMediaType(t *testing.T) {
	client := &rawCapableClient{}
	sessions := &fakeMailboxSessions{}
	authHash, hashErr := domainMail.HashPassword("correct-horse-battery")
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	h := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: authHash}, Sessions: sessions, Vault: webmail.NewMemoryVault(), Submitter: &fakeSubmitter{}}
	h.Dialer = &fakeDialer{wantAddress: "alice@example.test", client: client}
	r := newNativeTestRouter(t, h, sessions)
	token := loginNative(t, r, "alice@example.test", "correct-horse-battery")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/mailbox/folders/INBOX/import", strings.NewReader("hello"))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict || len(client.imported) != 0 || !strings.Contains(w.Body.String(), "MAIL_IMPORT_PREVIEW_REQUIRED") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestGetRawMessageNonCapableClientReturns501(t *testing.T) {
	sessions := &fakeMailboxSessions{}
	authHash, hashErr := domainMail.HashPassword("correct-horse-battery")
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	h := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: authHash}, Sessions: sessions, Vault: webmail.NewMemoryVault(), Submitter: &fakeSubmitter{}}
	h.Dialer = &fakeDialer{wantAddress: "alice@example.test", client: &fakeIMAPClient{message: &webmail.Message{MessageSummary: webmail.MessageSummary{UID: 5}}}}
	r := newNativeTestRouter(t, h, sessions)
	token := loginNative(t, r, "alice@example.test", "correct-horse-battery")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/folders/INBOX/messages/5/raw", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

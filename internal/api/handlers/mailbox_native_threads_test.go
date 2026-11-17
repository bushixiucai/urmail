package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type threadMembersCall struct {
	folder string
	uid    uint32
	opts   webmail.ThreadMembersOpts
}

// threadRecordingClient records the decoded folder/uid/opts every ThreadMembers
// request passes to the IMAP layer, so the handler test proves the folder path
// segment is decoded before the IMAP call.
type threadRecordingClient struct {
	fakeIMAPClient
	calls  []threadMembersCall
	result webmail.ThreadMembersResult
	err    error
}

func (c *threadRecordingClient) GetThreadMembers(_ context.Context, folder string, uid uint32, opts webmail.ThreadMembersOpts) (webmail.ThreadMembersResult, error) {
	c.calls = append(c.calls, threadMembersCall{folder: folder, uid: uid, opts: opts})
	if c.err != nil {
		return webmail.ThreadMembersResult{}, c.err
	}
	return c.result, nil
}

func newThreadMembersTestRouter(t *testing.T, h *MailboxNativeHandler, sessions middleware.MailboxSessionLookup) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(middleware.BodyLimitWithMailboxUploads(1<<20, 16<<20))
	r.GET("/api/v1/mailbox/status", h.Status)
	r.POST("/api/v1/mailbox/session", h.CreateSession)
	auth := r.Group("/api/v1/mailbox")
	auth.Use(middleware.RequireMailboxAuth(sessions))
	auth.GET("/folders/:name/messages/:uid/thread", h.ThreadMembers)
	return r
}

func threadMembersTestHandler(t *testing.T, client *threadRecordingClient) (*gin.Engine, string) {
	t.Helper()
	loginAttempts = newLoginLimiter()
	password := "correct-horse-battery"
	hash, err := mail.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	sessions := &fakeMailboxSessions{}
	dialer := &fakeDialer{wantAddress: "alice@example.test", client: client}
	h := &MailboxNativeHandler{
		Enabled:  true,
		Auth:     fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: hash},
		Sessions: sessions,
		Vault:    webmail.NewMemoryVault(),
		Dialer:   dialer,
	}
	r := newThreadMembersTestRouter(t, h, sessions)
	token := loginNative(t, r, "alice@example.test", password)
	return r, token
}

func getThreadMembers(t *testing.T, r *gin.Engine, token, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	return w
}

// TestThreadMembersDecodesEncodedFolder is the regression test for the v0.6.20
// 502: the web client always sends folders as b64.<base64url(name)>, and the
// handler must decode before calling IMAP. b64.U2VudA decodes to "Sent".
func TestThreadMembersDecodesEncodedFolder(t *testing.T) {
	client := &threadRecordingClient{result: webmail.ThreadMembersResult{
		ThreadID: 4, Total: 2, Scope: "folder",
		Messages: []webmail.MessageSummary{
			{UID: 4, Folder: "Sent", FolderEnc: webmail.EncodeFolderName("Sent")},
			{UID: 9, Folder: "INBOX", FolderEnc: webmail.EncodeFolderName("INBOX")},
		},
	}}
	r, token := threadMembersTestHandler(t, client)

	w := getThreadMembers(t, r, token, "/api/v1/mailbox/folders/b64.U2VudA/messages/4/thread?limit=20&anchor_uid=4")
	if w.Code != http.StatusOK {
		t.Fatalf("encoded Sent folder: status %d body %s", w.Code, w.Body.String())
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected 1 IMAP thread call, got %d", len(client.calls))
	}
	call := client.calls[0]
	if call.folder != "Sent" {
		t.Fatalf("decoded folder = %q, want %q", call.folder, "Sent")
	}
	if call.uid != 4 || call.opts.AnchorUID != 4 || call.opts.Limit != 20 {
		t.Fatalf("uid/anchor/limit not forwarded: %+v", call)
	}
	var body struct {
		Total    int  `json:"total"`
		Messages []struct {
			Folder string `json:"folder"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Total != 2 || len(body.Messages) != 2 || body.Messages[0].Folder != "Sent" {
		t.Fatalf("cross-folder members not passed through: %s", w.Body.String())
	}
}

// TestThreadMembersDecodesInboxEncodedFolder covers the Inbox half of the same
// bug: b64.SU5CT1g is the encoded INBOX name the client also sends.
func TestThreadMembersDecodesInboxEncodedFolder(t *testing.T) {
	client := &threadRecordingClient{result: webmail.ThreadMembersResult{ThreadID: 2651, Total: 1}}
	r, token := threadMembersTestHandler(t, client)

	w := getThreadMembers(t, r, token, "/api/v1/mailbox/folders/b64.SU5CT1g/messages/2651/thread?limit=20&anchor_uid=2651")
	if w.Code != http.StatusOK {
		t.Fatalf("encoded INBOX folder: status %d body %s", w.Code, w.Body.String())
	}
	if len(client.calls) != 1 || client.calls[0].folder != "INBOX" {
		t.Fatalf("decoded folder = %+v, want INBOX", client.calls)
	}
}

// TestThreadMembersRawNamePassThrough keeps the unencoded compatibility path.
func TestThreadMembersRawNamePassThrough(t *testing.T) {
	client := &threadRecordingClient{result: webmail.ThreadMembersResult{ThreadID: 4, Total: 1}}
	r, token := threadMembersTestHandler(t, client)

	w := getThreadMembers(t, r, token, "/api/v1/mailbox/folders/Sent/messages/4/thread?limit=20&anchor_uid=4")
	if w.Code != http.StatusOK {
		t.Fatalf("raw Sent folder: status %d body %s", w.Code, w.Body.String())
	}
	if len(client.calls) != 1 || client.calls[0].folder != "Sent" {
		t.Fatalf("raw folder not passed through: %+v", client.calls)
	}
}

// TestThreadMembersBeforeCursorForwarded covers the paging cursor path.
func TestThreadMembersBeforeCursorForwarded(t *testing.T) {
	client := &threadRecordingClient{result: webmail.ThreadMembersResult{ThreadID: 4, Total: 3}}
	r, token := threadMembersTestHandler(t, client)

	w := getThreadMembers(t, r, token, "/api/v1/mailbox/folders/b64.U2VudA/messages/4/thread?limit=20&before=INBOX%3A2")
	if w.Code != http.StatusOK {
		t.Fatalf("before cursor: status %d body %s", w.Code, w.Body.String())
	}
	if len(client.calls) != 1 || client.calls[0].opts.Before != "INBOX:2" {
		t.Fatalf("before cursor not forwarded: %+v", client.calls)
	}
}

// TestThreadMembersRejectsMalformedFolder: an undecodable b64 folder must be
// a 400, not the old opaque 502.
func TestThreadMembersRejectsMalformedFolder(t *testing.T) {
	client := &threadRecordingClient{}
	r, token := threadMembersTestHandler(t, client)

	w := getThreadMembers(t, r, token, "/api/v1/mailbox/folders/b64./messages/4/thread?limit=20&anchor_uid=4")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed folder: status %d body %s, want 400", w.Code, w.Body.String())
	}
	if len(client.calls) != 0 {
		t.Fatalf("malformed folder must not reach IMAP: %+v", client.calls)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error.Code != "INVALID_PARAMS" {
		t.Fatalf("malformed folder error code: %s", w.Body.String())
	}
}

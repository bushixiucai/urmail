package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func newSanitizeTestHandler(t *testing.T, msg *webmail.Message) (*MailboxNativeHandler, *fakeIMAPClient, func(method, path string) *httptest.ResponseRecorder) {
	t.Helper()
	loginAttempts = newLoginLimiter()
	password := "correct-horse-battery"
	hash, err := mail.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	sessions := &fakeMailboxSessions{}
	imap := &fakeIMAPClient{message: msg}
	h := &MailboxNativeHandler{
		Enabled:  true,
		Auth:     fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: hash},
		Sessions: sessions,
		Vault:    webmail.NewMemoryVault(),
		Dialer:   &fakeDialer{wantAddress: "alice@example.test", client: imap},
	}
	r := newNativeTestRouter(t, h, sessions)
	token := loginNative(t, r, "alice@example.test", password)
	return h, imap, func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		r.ServeHTTP(w, req)
		return w
	}
}

func TestGetMessageSanitizesHTMLRenderTime(t *testing.T) {
	msg := &webmail.Message{
		MessageSummary: webmail.MessageSummary{UID: 7, Subject: "Hi"},
		HTML: "<p>hi</p><script>alert(1)</script><img src=" + "\"" +
			"https://track.example/pixel.gif" + "\"" + "><img src=" + "\"" +
			"cid:logo@x" + "\"" + "><a href=" + "\"" +
			"javascript:alert(1)" + "\"" + ">x</a>",
		Attachments: []webmail.AttachmentMeta{
			{PartID: "3", Filename: "logo.png", ContentType: "image/png", Size: 3, ContentID: "logo@x"},
		},
	}
	msg2 := *msg
	msg2.HTML = "<p>hi</p><img src=\"https://track.example/pixel.gif\">"
	_, imap, auth := newSanitizeTestHandler(t, msg)
	folder := webmail.EncodeFolderName("INBOX")

	w := auth(http.MethodGet, "/api/v1/mailbox/folders/"+folder+"/messages/7")
	if w.Code != http.StatusOK {
		t.Fatalf("get %d %s", w.Code, w.Body.String())
	}
	var got struct {
		HTML  string   `json:"html"`
		Notes []string `json:"notes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	body := strings.ToLower(got.HTML)
	for _, banned := range []string{"<script", "alert(1)", "https://track.example", "javascript:"} {
		if strings.Contains(body, banned) {
			t.Fatalf("unsanitized output contains %q: %s", banned, got.HTML)
		}
	}
	// cid image rewritten to the same-origin attachment endpoint.
	if !strings.Contains(got.HTML, "/api/v1/mailbox/folders/INBOX/messages/7/attachments/3") {
		t.Fatalf("cid not rewritten: %s", got.HTML)
	}
	blocked := false
	for _, n := range got.Notes {
		if n == webmail.NoteRemoteImagesBlocked {
			blocked = true
		}
	}
	if !blocked {
		t.Fatalf("want remote_images_blocked note, got %v", got.Notes)
	}

	// Opt-in re-request allows remote images (per-message user action).
	// The fake returns the same pointer; restore the fixture so this
	// request exercises a fresh parse (the handler mutates the returned
	// view render-time, the IMAP original is untouched in production).
	imap.message = &msg2
	w = auth(http.MethodGet, "/api/v1/mailbox/folders/"+folder+"/messages/7?load_images=1")
	if w.Code != http.StatusOK {
		t.Fatalf("opt-in get %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(got.HTML, "https://track.example/pixel.gif") {
		t.Fatalf("opt-in remote image missing: %s", got.HTML)
	}
}

func TestAttachmentContentDispositionSanitized(t *testing.T) {
	cases := []struct{ name, in, wantSub, banned string }{
		{"traversal", "../../etc/passwd", "passwd", "../"},
		{"backslash", "..\\..\\win", "win", "..\\"},
		{"control chars", "bad\n\rname.txt", "badname.txt", "\n"},
		{"empty", "", "attachment; filename=attachment-2.1", ""},
		{"dot", ".", "attachment; filename=attachment-2.1", ""},
		{"utf8 encoded", "r\u00e9sum\u00e9.pdf", "pdf", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := attachmentContentDisposition(tc.in, "2.1")
			if !strings.Contains(got, tc.wantSub) {
				t.Fatalf("want %q in %q", tc.wantSub, got)
			}
			if tc.banned != "" && strings.Contains(got, tc.banned) {
				t.Fatalf("banned %q in %q", tc.banned, got)
			}
			if !strings.HasPrefix(got, "attachment") {
				t.Fatalf("not attachment disposition: %q", got)
			}
		})
	}
}

func TestCIDAttachmentURLMapping(t *testing.T) {
	atts := []webmail.AttachmentMeta{
		{PartID: "1.2", ContentID: "logo@x"},
		{PartID: "2"},
	}
	fn := cidAttachmentURL("INBOX", 9, atts)
	url, ok := fn("logo@x")
	if !ok || url != "/api/v1/mailbox/folders/INBOX/messages/9/attachments/1.2" {
		t.Fatalf("map: %q %v", url, ok)
	}
	if _, ok := fn("missing@x"); ok {
		t.Fatalf("unknown cid must not map")
	}
}

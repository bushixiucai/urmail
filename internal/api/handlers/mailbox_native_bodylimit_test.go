package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func TestMailboxNativeUploadBodyAndDecodedAttachmentLimits(t *testing.T) {
	token := "native-upload-fixture-token"
	hash := mailboxprincipal.HashToken(token)
	sessions := &fakeMailboxSessions{byHash: map[string]webmail.Session{hash: {TokenHash: hash, TenantID: 7, MailboxID: 9, Address: "alice@example.test", ExpiresAt: time.Now().Add(time.Hour)}}}
	vault := webmail.NewMemoryVault()
	vault.Put(hash, "fixture-password")
	imap := &fakeIMAPClient{}
	submit := &fakeSubmitter{}
	h := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{}, Sessions: sessions, Vault: vault, Dialer: &fakeDialer{wantAddress: "alice@example.test", client: imap}, Submitter: submit}
	router := newNativeTestRouter(t, h, sessions)
	for _, endpoint := range []struct{ method, path string }{{http.MethodPost, "/api/v1/mailbox/send"}, {http.MethodPut, "/api/v1/mailbox/drafts"}} {
		for _, tc := range []struct {
			name   string
			sizes  []int
			text   string
			status int
		}{
			{name: "single_5MiB", sizes: []int{5 << 20}, status: http.StatusOK},
			{name: "total_10MiB", sizes: []int{5 << 20, 5 << 20}, status: http.StatusOK},
			{name: "single_over_5MiB", sizes: []int{(5 << 20) + 1}, status: http.StatusRequestEntityTooLarge},
			{name: "total_over_10MiB", sizes: []int{5 << 20, 5 << 20, 1}, status: http.StatusRequestEntityTooLarge},
			{name: "transport_over_16MiB", text: strings.Repeat("x", (16<<20)+1), status: http.StatusRequestEntityTooLarge},
		} {
			t.Run(endpoint.method+endpoint.path+"/"+tc.name, func(t *testing.T) {
				submit.last = webmail.Outgoing{}
				imap.savedDraft = webmail.Outgoing{}
				atts := make([]mailboxSendAttachment, 0, len(tc.sizes))
				for _, size := range tc.sizes {
					atts = append(atts, mailboxSendAttachment{Filename: "file.bin", ContentType: "application/octet-stream", ContentB64: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, size))})
				}
				payload, err := json.Marshal(mailboxSendRequest{To: []string{"bob@example.test"}, Subject: "upload-limit", Text: tc.text, Attachments: atts})
				if err != nil {
					t.Fatal(err)
				}
				request := httptest.NewRequestWithContext(context.Background(), endpoint.method, endpoint.path, bytes.NewReader(payload))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Authorization", "Bearer "+token)
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				if response.Code != tc.status {
					t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
				}
				if tc.status == http.StatusRequestEntityTooLarge {
					if !strings.Contains(response.Body.String(), "MAILBOX_MESSAGE_TOO_LARGE") {
						t.Fatalf("wrong error: %s", response.Body.String())
					}
					if submit.last.Subject != "" || imap.savedDraft.Subject != "" {
						t.Fatal("oversized payload reached IMAP save or SMTP send")
					}
					return
				}
				outgoing := submit.last
				if endpoint.method == http.MethodPut {
					outgoing = imap.savedDraft
				}
				if outgoing.Subject != "upload-limit" || len(outgoing.Attachments) != len(tc.sizes) {
					t.Fatalf("handler did not receive full upload: %s", response.Body.String())
				}
				for index, size := range tc.sizes {
					if len(outgoing.Attachments[index].Content) != size {
						t.Fatalf("attachment %d bytes=%d want=%d", index, len(outgoing.Attachments[index].Content), size)
					}
				}
			})
		}
	}
}

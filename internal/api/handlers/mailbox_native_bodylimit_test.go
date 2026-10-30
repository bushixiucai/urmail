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

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func TestMailboxNativeUploadBodyAndDecodedAttachmentLimits(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(middleware.BodyLimitWithMailboxUploads(1<<20, 16<<20))
	var received webmail.Outgoing
	// Both durable send and draft handlers use these same transport/decoded guards.
	validate := func(c *gin.Context) {
		var req mailboxSendRequest
		if !bindMailboxMessage(c, &req) {
			return
		}
		atts, ok := decodeSendAttachments(c, req.Attachments)
		if !ok {
			return
		}
		received = webmail.Outgoing{Subject: req.Subject, Attachments: atts}
		c.Status(200)
	}
	router.POST("/api/v1/mailbox/send", validate)
	router.PUT("/api/v1/mailbox/drafts", validate)
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
				received = webmail.Outgoing{}
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
				request.Header.Set("Idempotency-Key", "upload-fixture-0001")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				if response.Code != tc.status {
					t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
				}
				if tc.status == http.StatusRequestEntityTooLarge {
					if !strings.Contains(response.Body.String(), "MAILBOX_MESSAGE_TOO_LARGE") {
						t.Fatalf("wrong error: %s", response.Body.String())
					}
					if received.Subject != "" {
						t.Fatal("oversized payload reached IMAP save or SMTP send")
					}
					return
				}
				outgoing := received
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

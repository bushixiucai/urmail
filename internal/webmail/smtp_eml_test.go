package webmail

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
)

func TestForwardedRFC822UsesIdentityEncodingAndExactBytes(t *testing.T) {
	original := []byte("From: original@example.test\r\nTo: alice@example.test\r\nSubject: Original\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nOriginal body \xc3\xa9\r\n")
	raw := buildMIME("alice@example.test", Outgoing{To: []string{"alice@example.test"}, Subject: "Forward", Text: "Forwarded message", Attachments: []OutgoingAttachment{{Filename: "original.eml", ContentType: "message/rfc822", Content: original}}}, false)
	message, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	_, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(message.Body, params["boundary"])
	if _, err = reader.NextPart(); err != nil {
		t.Fatal(err)
	}
	part, err := reader.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if part.Header.Get("Content-Transfer-Encoding") != "8bit" {
		t.Fatal("message/rfc822 must use identity transfer encoding")
	}
	content, err := io.ReadAll(part)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, original) {
		t.Fatalf("forward changed original MIME: %q", content)
	}
	nested, err := mail.ReadMessage(bytes.NewReader(content))
	if err != nil || nested.Header.Get("Subject") != "Original" {
		t.Fatalf("standard nested MIME parsing failed: %v", err)
	}
}

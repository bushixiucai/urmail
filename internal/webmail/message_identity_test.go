package webmail

import (
	"bytes"
	"context"
	"net/mail"
	"strings"
	"testing"
)

func TestFrozenMIMEHasStableIdentityDateAndCanonicalNewlines(t *testing.T) {
	out := FreezeOutgoing(Outgoing{From: "alice@example.test", To: []string{"bob@example.test"}, Bcc: []string{"hidden@example.test"}, Subject: "中文标题", Text: "first\nsecond"})
	out.Raw = []byte(BuildMIME(out.From, out))
	first := BuildMIME(out.From, out)
	second := BuildMIME(out.From, out)
	if first != second {
		t.Fatal("frozen message bytes changed")
	}
	parsed, err := mail.ReadMessage(strings.NewReader(first))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Header.Get("Message-ID") != out.MessageID || parsed.Header.Get("Date") == "" {
		t.Fatal("missing stable message headers")
	}
	if _, err = mail.ParseDate(parsed.Header.Get("Date")); err != nil {
		t.Fatal(err)
	}
	if parsed.Header.Get("Bcc") != "" {
		t.Fatal("SMTP leaked Bcc")
	}
	if strings.Contains(strings.ReplaceAll(first, "\r\n", ""), "\n") || !strings.HasSuffix(first, "\r\n") {
		t.Fatal("frozen bytes differ from SMTP canonical form")
	}
}
func TestScheduledDraftPreservesCIDAndRefusesTruncatedAttachments(t *testing.T) {
	client := &fakeDraftClient{msg: &Message{MessageSummary: MessageSummary{UID: 9}, To: []Address{{Email: "bob@example.test"}}, HTML: `<img src="cid:logo@example.test">`, Attachments: []AttachmentMeta{{PartID: "2", Filename: "logo.png", ContentType: "image/png", ContentID: "logo@example.test", Inline: true}}}, atts: map[string][]byte{"2": []byte("png-fixture")}}
	out, err := OutgoingFromDraft(context.Background(), client, "Drafts", 9)
	if err != nil {
		t.Fatal(err)
	}
	raw := BuildMIME("alice@example.test", out)
	if !strings.Contains(raw, "Content-ID: <logo@example.test>") || !out.Attachments[0].Inline || !bytes.Equal(out.Attachments[0].Content, []byte("png-fixture")) {
		t.Fatal("CID resource was not preserved")
	}
	client.msg.AttachmentsCapped = true
	if _, err = OutgoingFromDraft(context.Background(), client, "Drafts", 9); err == nil {
		t.Fatal("truncated draft silently dropped attachments")
	}
}

func TestMIMEAttachmentDispositionAndCharsetArePreserved(t *testing.T) {
	for _, inline := range []bool{false, true} {
		original := BuildDraftMIME("alice@example.test", Outgoing{To: []string{"bob@example.test"}, HTML: `<img src="cid:logo@fixture">`, Attachments: []OutgoingAttachment{{Filename: "logo.png", ContentType: "image/png", ContentID: "logo@fixture", Inline: inline, Content: []byte("png-fixture")}, {Filename: "legacy.txt", ContentType: "text/plain; charset=iso-8859-1", Content: []byte{0xe9}}}})
		text, html, atts, err := parseMIMEBodyStrict([]byte(original))
		if err != nil {
			t.Fatal(err)
		}
		if len(atts) != 2 || atts[0].Inline != inline || atts[0].ContentID != "logo@fixture" || !strings.Contains(atts[1].ContentType, "iso-8859-1") {
			t.Fatal("MIME metadata fabricated disposition or dropped charset")
		}
		client := &fakeDraftClient{msg: &Message{MessageSummary: MessageSummary{UID: 9}, To: []Address{{Email: "bob@example.test"}}, Text: text, HTML: html, Attachments: atts}, atts: map[string][]byte{atts[0].PartID: []byte("png-fixture"), atts[1].PartID: {0xe9}}}
		rebuilt, err := OutgoingFromDraft(context.Background(), client, "Drafts", 9)
		if err != nil {
			t.Fatal(err)
		}
		if rebuilt.Attachments[0].Inline != inline || !strings.Contains(rebuilt.Attachments[1].ContentType, "iso-8859-1") {
			t.Fatal("scheduled reconstruction changed original disposition or charset")
		}
	}
}

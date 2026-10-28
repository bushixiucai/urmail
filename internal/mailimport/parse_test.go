package mailimport

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
)

func validEML(subject, body string) []byte {
	return []byte("From: sender@example.test\r\nTo: owner@example.test\r\nSubject: " + subject + "\r\nMessage-ID: <stable@example.test>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + body)
}
func one(t *testing.T, raw []byte) Item {
	t.Helper()
	p, e := Parse("eml", "message.eml", raw)
	if e != nil || len(p.Items) != 1 {
		t.Fatal(p, e)
	}
	return p.Items[0]
}
func mbox(messages ...[]byte) []byte {
	var out bytes.Buffer
	for _, message := range messages {
		out.WriteString("From sender@example.test Fri Jun 23 02:56:55 2000\n")
		out.Write(message)
		out.WriteString("\r\n")
	}
	return out.Bytes()
}

func TestEMLCanonicalIdentityMetadataAndCharset(t *testing.T) {
	raw := validEML("=?ISO-8859-1?Q?Ol=E1?=", "你好世界\r\n")
	original := bytes.Clone(raw)
	preview, e := Parse("EML", `C:\private\mail.eml`, raw)
	if e != nil {
		t.Fatal(e)
	}
	item := preview.Items[0]
	if item.ErrorCode != "" || item.Subject != "Olá" || item.MessageID != "<stable@example.test>" || !bytes.Equal(item.Raw, raw) || item.SHA256 != digest(raw) || item.Size != int64(len(raw)) {
		t.Fatal(item)
	}
	if preview.SourceDigest != digest(original) || preview.Filename != "mail.eml" || preview.Format != "eml" || !bytes.Equal(raw, original) {
		t.Fatal(preview)
	}
	encoded, e := json.Marshal(preview)
	if e != nil || bytes.Contains(encoded, []byte("你好世界")) || bytes.Contains(encoded, []byte(`"Raw"`)) {
		t.Fatal("preview leaked raw message", e)
	}
	lf := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	normalized := one(t, lf)
	if normalized.ErrorCode != "" || !bytes.Equal(normalized.Raw, raw) || normalized.SHA256 != item.SHA256 {
		t.Fatal("LF normalization changed the canonical identity", normalized)
	}
	missing := []byte("Subject: No message identity\r\n\r\nBody\r\n")
	noID := one(t, missing)
	if noID.ErrorCode != "" || noID.MessageID != "" || !bytes.Equal(noID.Raw, missing) {
		t.Fatal("missing ID was fabricated or rejected", noID)
	}
	latin := []byte("Subject: =?windows-1252?Q?caf=E9?=\r\nContent-Type: text/plain; charset=windows-1252\r\n\r\ncaf\xe9\r\n")
	if item := one(t, latin); item.ErrorCode != "" || item.Subject != "café" || !bytes.Equal(item.Raw, latin) {
		t.Fatal("declared legacy charset was not preserved", item)
	}
}

func TestMBOXMixedItemsDuplicatesAndFromEscaping(t *testing.T) {
	raw := validEML("Thread", "From someone wrote a reply\r\n>From quoted\r\n>>From twice\r\n\r\n")
	exported, e := ExportMBOX([][]byte{raw, raw})
	if e != nil {
		t.Fatal(e)
	}
	p, e := Parse("mbox", "/private/source.mbox", exported)
	if e != nil || len(p.Items) != 2 {
		t.Fatal(p, e)
	}
	for _, item := range p.Items {
		if item.ErrorCode != "" || !bytes.Equal(item.Raw, raw) {
			t.Fatal("mboxrd roundtrip lost original bytes", item)
		}
	}
	if p.Items[0].DuplicateOf != 0 || p.Items[1].DuplicateOf != 1 {
		t.Fatal("duplicates did not reference original stable index", p.Items)
	}
	if !bytes.Contains(exported, []byte("\r\n>From someone")) || !bytes.Contains(exported, []byte("\r\n>>From quoted")) || !bytes.Contains(exported, []byte("\r\n>>>From twice")) {
		t.Fatal("missing recursive mboxrd escaping")
	}
	mixed := mbox(raw, []byte("not a valid header\r\n\r\nbad\r\n"), raw)
	p, e = Parse("mbox", "mixed.mbox", mixed)
	if e != nil || len(p.Items) != 3 {
		t.Fatal(p, e)
	}
	if p.Items[0].ErrorCode != "" || p.Items[1].ErrorCode != CodeInvalidHeaders || len(p.Items[1].Raw) != 0 || p.Items[1].SHA256 != "" || p.Items[2].Index != 3 || p.Items[2].DuplicateOf != 1 {
		t.Fatal("partial errors or indices lost", p.Items)
	}
	if _, e = Parse("mbox", "bad.mbox", append([]byte("preamble\n"), mixed...)); !errors.Is(e, ErrInvalidSource) {
		t.Fatal("unreviewed prefix accepted", e)
	}
}

func TestMultipartAttachmentOctetsAndRawRoundtrip(t *testing.T) {
	attachment := []byte{0, 255, 1, 13, 10, 62, 70, 114, 111, 109, 32, 128}
	raw := []byte("Subject: Attachment\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=example-boundary\r\n\r\n--example-boundary\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nBody\r\n--example-boundary\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=sample.bin\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(attachment) + "\r\n--example-boundary--\r\n")
	item := one(t, raw)
	if item.ErrorCode != "" {
		t.Fatal(item.ErrorCode)
	}
	exported, e := ExportMBOX([][]byte{raw})
	if e != nil {
		t.Fatal(e)
	}
	p, e := Parse("mbox", "attachment.mbox", exported)
	if e != nil || len(p.Items) != 1 || p.Items[0].SHA256 != item.SHA256 {
		t.Fatal("raw attachment MIME hash changed", p, e)
	}
	message, e := mail.ReadMessage(bytes.NewReader(p.Items[0].Raw))
	if e != nil {
		t.Fatal(e)
	}
	reader := multipart.NewReader(message.Body, "example-boundary")
	if _, e = reader.NextRawPart(); e != nil {
		t.Fatal(e)
	}
	part, e := reader.NextRawPart()
	if e != nil {
		t.Fatal(e)
	}
	data, e := io.ReadAll(base64.NewDecoder(base64.StdEncoding, part))
	if e != nil || !bytes.Equal(data, attachment) {
		t.Fatal("binary attachment octets changed", data, e)
	}
	binary := append([]byte("Subject: Binary\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: binary\r\n\r\n"), []byte{0, 255, 10, 13, 1}...)
	if item = one(t, binary); item.ErrorCode != "" || !bytes.Equal(item.Raw, binary) {
		t.Fatal("explicit binary EML changed", item.ErrorCode)
	}
	if _, e = ExportMBOX([][]byte{binary}); !errors.Is(e, ErrMBOXFraming) {
		t.Fatal("lossy binary mbox export allowed", e)
	}
	// A textual mention of a binary header is not a binary MIME entity.
	text := validEML("Documentation", "Content-Transfer-Encoding: binary\r\n")
	if _, e = ExportMBOX([][]byte{text}); e != nil {
		t.Fatal("body text mistaken for MIME header", e)
	}
}

func TestPerItemMIMEAndHeaderRefusals(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		code string
	}{
		{"header-null", []byte("Subject: hi\x00there\r\n\r\nbody\r\n"), CodeInvalidHeaders},
		{"header-bare-cr", []byte("Subject: hi\rBcc: victim@example.test\r\n\r\nbody\r\n"), CodeInvalidHeaders},
		{"duplicate-type", []byte("Content-Type: text/plain\r\nContent-Type: text/html\r\n\r\nbody\r\n"), CodeInvalidHeaders},
		{"oversized-header", []byte("Subject: " + strings.Repeat("s", maxHeaderBytes) + "\r\n\r\nbody\r\n"), CodeInvalidHeaders},
		{"invalid-base64", []byte("Subject: Invalid\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n\r\n!!!!\r\n"), CodeInvalidEncoding},
		{"unknown-charset", []byte("Subject: Charset\r\nContent-Type: text/plain; charset=unknown-evil-charset\r\n\r\nbody\r\n"), CodeUnsupportedCharset},
		{"invalid-utf8", []byte("Subject: UTF8\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n\xff\r\n"), CodeInvalidEncoding},
		{"missing-boundary", []byte("Subject: Multipart\r\nContent-Type: multipart/mixed\r\n\r\nbody\r\n"), CodeInvalidMIME},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := one(t, tc.raw)
			if item.ErrorCode != tc.code || item.Raw != nil || item.SHA256 != "" {
				t.Fatalf("got %+v want %s", item, tc.code)
			}
		})
	}
	damaged := []byte("Subject: Broken multipart\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\nContent-Type: text/plain\r\n\r\nbody without closing delimiter")
	if item := one(t, damaged); item.ErrorCode == "" || item.Raw != nil {
		t.Fatal("truncated multipart silently accepted", item)
	}
}

func TestStructureAndSourceBudgetsNeverTruncate(t *testing.T) {
	body := []byte("Content-Type: text/plain\r\n\r\nleaf\r\n")
	for depth := 0; depth < maxMIMEDepth+2; depth++ {
		boundary := fmt.Sprintf("depth%d", depth)
		body = []byte("Content-Type: multipart/mixed; boundary=" + boundary + "\r\n\r\n--" + boundary + "\r\n" + string(body) + "\r\n--" + boundary + "--\r\n")
	}
	if item := one(t, body); item.ErrorCode != CodeStructureLimit {
		t.Fatal("deep MIME accepted", item.ErrorCode)
	}
	var parts strings.Builder
	parts.WriteString("Content-Type: multipart/mixed; boundary=x\r\n\r\n")
	for i := 0; i < maxMIMEParts+1; i++ {
		parts.WriteString("--x\r\nContent-Type: text/plain\r\n\r\nx\r\n")
	}
	parts.WriteString("--x--\r\n")
	if item := one(t, []byte(parts.String())); item.ErrorCode != CodeStructureLimit {
		t.Fatal("excessive MIME parts accepted", item.ErrorCode)
	}
	if item := one(t, append(validEML("Large", ""), bytes.Repeat([]byte{'x'}, MaxMessageBytes)...)); item.ErrorCode != CodeMessageTooLarge || item.Raw != nil {
		t.Fatal("oversized item accepted", item.ErrorCode)
	}
	if _, e := Parse("eml", "large.eml", make([]byte, MaxSourceBytes+1)); !errors.Is(e, ErrSourceTooLarge) {
		t.Fatal(e)
	}
	if _, e := Parse("zip", "archive.zip", []byte("data")); !errors.Is(e, ErrUnsupportedFormat) {
		t.Fatal(e)
	}
	messages := make([][]byte, MaxItems+1)
	for i := range messages {
		messages[i] = validEML("Message", "body\r\n")
	}
	if _, e := Parse("mbox", "too-many.mbox", mbox(messages...)); !errors.Is(e, ErrTooManyItems) {
		t.Fatal("silently truncated mbox item list", e)
	}
	if _, e := ExportMBOX(messages); !errors.Is(e, ErrTooManyItems) {
		t.Fatal(e)
	}
	if _, e := ExportMBOX([][]byte{validEML("No final EOL", "body")}); !errors.Is(e, ErrMBOXFraming) {
		t.Fatal("unterminated body silently changed", e)
	}
}

func FuzzBoundedMailSources(f *testing.F) {
	f.Add(byte(0), []byte("Subject: Seed\r\n\r\nbody\r\n"))
	f.Add(byte(1), mbox(validEML("Seed", "body\r\n")))
	f.Add(byte(0), []byte("Content-Type: multipart/mixed; boundary=x\n\n--x\n\nbody"))
	f.Fuzz(func(t *testing.T, mode byte, source []byte) {
		if len(source) > 64<<10 {
			t.Skip()
		}
		original := bytes.Clone(source)
		format := "eml"
		if mode&1 != 0 {
			format = "mbox"
		}
		preview, err := Parse(format, "fuzz.mail", source)
		if !bytes.Equal(source, original) {
			t.Fatal("parser mutated source bytes")
		}
		if err != nil {
			return
		}
		if len(preview.Items) > MaxItems || preview.SourceDigest != digest(source) {
			t.Fatal("preview escaped source bounds")
		}
		for _, item := range preview.Items {
			if item.ErrorCode != "" {
				if len(item.Raw) != 0 || item.SHA256 != "" {
					t.Fatal("invalid item retained usable bytes")
				}
			} else if item.Size != int64(len(item.Raw)) || len(item.Raw) > MaxMessageBytes || item.SHA256 != digest(item.Raw) {
				t.Fatal("accepted item identity inconsistent")
			}
		}
	})
}

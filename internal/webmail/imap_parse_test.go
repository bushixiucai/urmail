package webmail

import (
	"strings"
	"testing"
	"time"
)

func TestParseEnvelope(t *testing.T) {
	env := `("17-Oct-2026 10:00:00 +0000" "Hello" (("Alice" NIL "alice" "example.test")) NIL NIL (("Bob" NIL "bob" "example.test")) NIL NIL NIL "<id@example.test>")`
	subject, from, date := parseEnvelope(env)
	if subject != "Hello" {
		t.Fatalf("subject=%q", subject)
	}
	if len(from) != 1 || from[0].Email != "alice@example.test" || from[0].Name != "Alice" {
		t.Fatalf("from=%#v", from)
	}
	if !date.Equal(time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("date=%v", date)
	}
}

func TestParseEnvelopeReplyTo(t *testing.T) {
	env := "(\"17-Oct-2026 10:00:00 +0000\" \"Hello\" ((\"Alice\" NIL \"alice\" \"example.test\")) ((\"Alice\" NIL \"alice\" \"example.test\")) ((\"list\" NIL \"list\" \"example.test\")) ((\"Bob\" NIL \"bob\" \"example.test\")) NIL NIL NIL \"<id@example.test>\")"
	_, from, to, cc, replyTo, _ := parseEnvelopeParties(env)
	if len(from) != 1 || from[0].Email != "alice@example.test" {
		t.Fatalf("from=%#v", from)
	}
	if len(replyTo) != 1 || replyTo[0].Email != "list@example.test" {
		t.Fatalf("reply-to=%#v", replyTo)
	}
	if len(to) != 1 || to[0].Email != "bob@example.test" {
		t.Fatalf("to=%#v", to)
	}
	if len(cc) != 0 {
		t.Fatalf("cc=%#v", cc)
	}
}

func TestParseFetchAttrsAndSummary(t *testing.T) {
	payload := `(UID 42 FLAGS (\Seen \Flagged) RFC822.SIZE 321 ENVELOPE ("17-Oct-2026 10:00:00 +0000" "Hi" (("A" NIL "a" "b.test")) NIL NIL NIL NIL NIL NIL NIL))`
	attrs := parseFetchAttrs(payload)
	sum, err := summaryFromAttrs(attrs)
	if err != nil {
		t.Fatal(err)
	}
	if sum.UID != 42 || sum.Size != 321 || sum.Subject != "Hi" {
		t.Fatalf("%+v", sum)
	}
	if strings.Join(sum.Flags, ",") != `\Seen,\Flagged` {
		t.Fatalf("flags=%v", sum.Flags)
	}
}

func TestParseMIMEBodyMultipart(t *testing.T) {
	raw := "MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=bnd\r\n\r\n" +
		"--bnd\r\nContent-Type: text/plain\r\n\r\nhello\r\n" +
		"--bnd\r\nContent-Type: text/html\r\n\r\n<p>hi</p>\r\n" +
		"--bnd\r\nContent-Type: application/pdf; name=\"a.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"a.pdf\"\r\n\r\nPDFDATA\r\n" +
		"--bnd--\r\n"
	text, html, atts := parseMIMEBody([]byte(raw))
	if text != "hello" || html != "<p>hi</p>" {
		t.Fatalf("text=%q html=%q", text, html)
	}
	if len(atts) != 1 || atts[0].Filename != "a.pdf" || atts[0].PartID != "3" {
		t.Fatalf("atts=%#v", atts)
	}
}

func TestExtractMIMEPartBase64(t *testing.T) {
	// "PDFDATA" base64-encoded
	raw := "MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=bnd\r\n\r\n" +
		"--bnd\r\nContent-Type: text/plain\r\n\r\nhello\r\n" +
		"--bnd\r\nContent-Type: application/pdf; name=\"a.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"a.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		"UERGREFUQQ==\r\n" +
		"--bnd--\r\n"
	filename, ct, data, err := extractMIMEPart([]byte(raw), "2")
	if err != nil {
		t.Fatal(err)
	}
	if filename != "a.pdf" || ct != "application/pdf" {
		t.Fatalf("meta filename=%q ct=%q", filename, ct)
	}
	if string(data) != "PDFDATA" {
		t.Fatalf("data=%q", data)
	}
	_, _, _, err = extractMIMEPart([]byte(raw), "9")
	if err == nil {
		t.Fatal("expected missing part")
	}
	text, _, atts := parseMIMEBody([]byte(raw))
	if text != "hello" || len(atts) != 1 || atts[0].PartID != "2" || atts[0].Size != int64(len("PDFDATA")) {
		t.Fatalf("parse text=%q atts=%#v", text, atts)
	}
}

func TestParseUIDSearch(t *testing.T) {
	uids := parseUIDSearch([]string{"* SEARCH 1 2 9", "* OK ignored"})
	if len(uids) != 3 || uids[2] != 9 {
		t.Fatalf("%v", uids)
	}
}

func TestSummaryAttachmentsUseMIMEMetadataNotSize(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		size       string
		want       bool
	}{
		{"large plain", `("TEXT" "PLAIN" ("CHARSET" "UTF-8") NIL NIL "7BIT" 80000 900 NIL NIL NIL)`, "80000", false},
		{"small attachment", `(("TEXT" "PLAIN" NIL NIL NIL "7BIT" 2 1)("APPLICATION" "PDF" ("NAME" "invoice.pdf") NIL NIL "BASE64" 10 NIL ("ATTACHMENT" ("FILENAME" "invoice.pdf"))) "MIXED")`, "50", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row, err := summaryFromAttrs(map[string]string{"UID": "1", "RFC822.SIZE": tc.size, "BODYSTRUCTURE": tc.body})
			if err != nil || row.HasAttachments != tc.want {
				t.Fatalf("row=%+v err=%v want attachment=%v", row, err, tc.want)
			}
		})
	}
}

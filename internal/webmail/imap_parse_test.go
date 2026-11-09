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
	_, from, replyTo, _ := parseEnvelopeParties(env)
	if len(from) != 1 || from[0].Email != "alice@example.test" {
		t.Fatalf("from=%#v", from)
	}
	if len(replyTo) != 1 || replyTo[0].Email != "list@example.test" {
		t.Fatalf("reply-to=%#v", replyTo)
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

// THREAD-UX-REVIEW F3: single-digit-day sender dates (with or without a
// trailing timezone comment) must parse instead of rendering as an em dash.
func TestParseIMAPDateVariants(t *testing.T) {
	cases := []struct {
		in   string
		want string // RFC3339 in UTC, empty for unparsable
	}{
		{"Thu, 9 Oct 2026 20:31:41 +0800", "2026-10-09T12:31:41Z"},
		{"Thu, 9 Oct 2026 20:31:41 +0800 (CST)", "2026-10-09T12:31:41Z"},
		{"9 Oct 2026 20:31:41 +0800", "2026-10-09T12:31:41Z"},
		{"Thu, 09 Oct 2026 20:31:41 +0800", "2026-10-09T12:31:41Z"},
		{"09-Oct-2026 20:31:41 +0800", "2026-10-09T12:31:41Z"},
		{"not a date", ""},
		{"", ""},
	}
	for _, tc := range cases {
		got := parseIMAPDate(tc.in)
		if tc.want == "" {
			if !got.IsZero() {
				t.Fatalf("parseIMAPDate(%q) = %v, want zero", tc.in, got)
			}
			continue
		}
		if got.UTC().Format(time.RFC3339) != tc.want {
			t.Fatalf("parseIMAPDate(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// THREAD-UX-REVIEW F4: garbled encoded-word headers must surface decoded
// text, never the raw encoded-word.
func TestAddressesFromHeaderEncodedWord(t *testing.T) {
	// Well-formed display name: net/mail decodes it.
	got := addressesFromHeader("=?utf-8?B?VFVUVS5UTw==?= <verify@tutu.to>")
	if len(got) != 1 || got[0].Name != "TUTU.TO" || got[0].Email != "verify@tutu.to" {
		t.Fatalf("well-formed: %#v", got)
	}
	// Encoded-word only, no addr-spec: salvaged as a display-only address.
	got = addressesFromHeader("=?utf-8?B?VFVUVS5UTw==?=")
	if len(got) != 1 || got[0].Name != "TUTU.TO" || got[0].Email != "" {
		t.Fatalf("encoded-word only: %#v", got)
	}
	// Mixed: the good address survives its garbled sibling.
	got = addressesFromHeader(`real@example.test, =?utf-8?B?VFVUVS5UTw==?=`)
	if len(got) != 2 || got[0].Email != "real@example.test" || got[1].Name != "TUTU.TO" || got[1].Email != "" {
		t.Fatalf("mixed: %#v", got)
	}
	if got := addressesFromHeader(""); got != nil {
		t.Fatalf("empty: %#v", got)
	}
}

// THREAD-UX-REVIEW F4: an ENVELOPE address with an encoded-word mailbox and
// no host becomes a display-only address instead of being dropped.
func TestParseAddressEncodedMailboxNoHost(t *testing.T) {
	addr, ok := parseAddress(`(NIL NIL "=?utf-8?B?VFVUVS5UTw==?=" NIL)`)
	if !ok || addr.Name != "TUTU.TO" || addr.Email != "" {
		t.Fatalf("addr=%#v ok=%v", addr, ok)
	}
	// undisclosed-recipients style group addresses stay visible too.
	addr, ok = parseAddress(`(NIL NIL "undisclosed-recipients" NIL)`)
	if !ok || addr.Name != "undisclosed-recipients" || addr.Email != "" {
		t.Fatalf("group addr=%#v ok=%v", addr, ok)
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

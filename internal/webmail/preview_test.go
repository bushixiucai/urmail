package webmail

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSnippetFromPlainTextRules(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{
			name: "single line body",
			text: "Hello world",
			want: "Hello world",
		},
		{
			name: "multi line body keeps the first two lines",
			text: "First line\nSecond line\nThird line",
			want: "First line Second line",
		},
		{
			name: "blank lines do not stop line collection",
			text: "First line\n\nSecond line\n\nThird line",
			want: "First line Second line",
		},
		{
			name: "whitespace collapses",
			text: "  Leading  spaces\tand\ttabs  \n  more\tspacing \n end",
			want: "Leading spaces and tabs more spacing",
		},
		{
			name: "quote header removed, quoted lines removed",
			text: "On Mon, 12 Oct 2026 at 10:00:00, Bob <bob@example.test> wrote:\n> old text\n> more old text\nNew reply line",
			want: "New reply line",
		},
		{
			name: "reply before quote header keeps the reply",
			text: "Hi there,\n\nOn Mon, 12 Oct 2026 at 10:00:00, Bob <bob@example.test> wrote:\n> quoted body\n",
			want: "Hi there,",
		},
		{
			name: "gt quoted lines removed",
			text: "> quoted first\nReply text\n> quoted again",
			want: "Reply text",
		},
		{
			name: "original message separator stops the snippet",
			text: "Hi there,\n-----Original Message-----\nFrom: Bob\nSent: Monday\nSubject: Re: X\nOld body text",
			want: "Hi there,",
		},
		{
			name: "signature delimiter stops the snippet",
			text: "Body line one\nBody line two\n-- \nAlice Example\nalice@example.test",
			want: "Body line one Body line two",
		},
		{
			name: "signature after a single line keeps the line",
			text: "Short body\n-- \nAlice Example",
			want: "Short body",
		},
		{
			name: "cid reference removed from plain text",
			text: "See the chart [cid:chart123@example.test] below\nSecond line",
			want: "See the chart below Second line",
		},
		{
			name: "empty body gives empty snippet",
			text: "",
			want: "",
		},
		{
			name: "quote only body gives empty snippet",
			text: "> quoted text only\n> more quotes",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := snippetFromPlainText(tc.text); got != tc.want {
				t.Fatalf("snippetFromPlainText(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestSnippetTruncatesLongTextAtWordBoundary(t *testing.T) {
	long := strings.Repeat("word ", 60) // 300 runes, all words five chars
	got := snippetFromPlainText(long)
	if runes := utf8.RuneCountInString(got); runes > previewMaxChars {
		t.Fatalf("snippet length %d exceeds %d", runes, previewMaxChars)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated snippet %q does not end with an ellipsis", got)
	}
	// The cut must land on a word boundary: no fragment of a word survives.
	if fragment := strings.TrimSuffix(strings.TrimSuffix(got, "…"), " "); strings.HasSuffix(long, fragment) && !strings.HasSuffix(fragment, "word") {
		t.Fatalf("snippet %q cut inside a word", got)
	}
	if got := snippetFromPlainText(strings.Repeat("w", previewMaxChars-1)); got != strings.Repeat("w", previewMaxChars-1) {
		t.Fatal("text shorter than the limit must stay unchanged")
	}
}

func TestPreviewFromRawPrefersPlainText(t *testing.T) {
	raw := []byte("Subject: mixed\r\n" +
		"Content-Type: multipart/alternative; boundary=\"xyz\"\r\n" +
		"\r\n" +
		"--xyz\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<html><body><p>HTML body first</p></body></html>\r\n" +
		"--xyz\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Plain body wins\r\n" +
		"--xyz--\r\n")
	if got := previewFromRaw(raw); got != "Plain body wins" {
		t.Fatalf("preview = %q, want plain text part", got)
	}
}

func TestPreviewFromRawHTMLFallback(t *testing.T) {
	raw := []byte("Subject: html only\r\n" +
		"Content-Type: multipart/related; boundary=\"b1\"\r\n" +
		"\r\n" +
		"--b1\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<html><body><p>Meeting notes</p><img src=\"cid:logo@example.test\" alt=\"logo\">Follow-up line</body></html>\r\n" +
		"--b1\r\n" +
		"Content-Type: image/png\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"Content-ID: <logo@example.test>\r\n" +
		"\r\n" +
		"aGVsbG8=\r\n" +
		"--b1--\r\n")
	got := previewFromRaw(raw)
	if got != "Meeting notes Follow-up line" {
		t.Fatalf("preview = %q", got)
	}
	if strings.Contains(got, "cid:") {
		t.Fatalf("cid reference leaked into preview %q", got)
	}
}

func TestPreviewFromRawHTMLOnlyTopLevel(t *testing.T) {
	raw := []byte("Content-Type: text/html; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"\r\n" +
		"<p>Status update: everything is fine</p><p>Second paragraph</p>")
	if got := previewFromRaw(raw); got != "Status update: everything is fine Second paragraph" {
		t.Fatalf("preview = %q", got)
	}
}

func TestPreviewFromRawQuotedPrintableAndCharset(t *testing.T) {
	// GBK bytes for "你好" are decoded to UTF-8 before the snippet rules run.
	gbk := []byte{0xC4, 0xE3, 0xBA, 0xC3}
	raw := []byte("Content-Type: text/plain; charset=gbk\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"")
	raw = append(raw, []byte(base64StdEncoded(gbk)+"\r\n")...)
	if got := previewFromRaw(raw); got != "你好" {
		t.Fatalf("preview = %q, want decoded GBK greeting", got)
	}
}

func TestPreviewFromRawTruncatedMultipart(t *testing.T) {
	// The chunk cuts inside the first part: the snippet still renders.
	raw := []byte("Content-Type: multipart/mixed; boundary=\"cut\"\r\n" +
		"\r\n" +
		"--cut\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"Truncated body that continues beyond the fetched bytes without end")
	got := previewFromRaw(raw)
	if !strings.HasPrefix(got, "Truncated body that continues") {
		t.Fatalf("preview = %q", got)
	}
}

func TestPreviewFromRawNonTextPartYieldsEmpty(t *testing.T) {
	raw := []byte("Content-Type: application/pdf\r\n\r\n%PDF-1.4 binary")
	if got := previewFromRaw(raw); got != "" {
		t.Fatalf("preview = %q, want empty for a non-text body", got)
	}
}

func TestPreviewFromRawEmptyAndHeaderOnly(t *testing.T) {
	if got := previewFromRaw(nil); got != "" {
		t.Fatalf("nil message preview = %q", got)
	}
	if got := previewFromRaw([]byte("Subject: headers only\r\n")); got != "" {
		t.Fatalf("header-only preview = %q", got)
	}
	if got := previewFromRaw([]byte("Subject: empty body\r\n\r\n")); got != "" {
		t.Fatalf("empty body preview = %q", got)
	}
}

// base64StdEncoded is a tiny helper for the charset test.
func base64StdEncoded(b []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var out strings.Builder
	for i := 0; i < len(b); i += 3 {
		var chunk [3]byte
		copy(chunk[:], b[i:])
		n := len(b) - i
		out.WriteByte(alphabet[chunk[0]>>2])
		out.WriteByte(alphabet[(chunk[0]&0x03)<<4|chunk[1]>>4])
		if n > 1 {
			out.WriteByte(alphabet[(chunk[1]&0x0f)<<2|chunk[2]>>6])
		} else {
			out.WriteByte('=')
		}
		if n > 2 {
			out.WriteByte(alphabet[chunk[2]&0x3f])
		} else {
			out.WriteByte('=')
		}
	}
	return out.String()
}

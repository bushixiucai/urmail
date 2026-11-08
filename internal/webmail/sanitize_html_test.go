package webmail

import (
	"errors"
	"strings"
	"testing"
)

func mustSanitize(t *testing.T, in string) (string, []string) {
	t.Helper()
	out, notes := SanitizeHTML(in)
	return out, notes
}

// TestSanitizeHTMLHostileCorpus covers mXSS patterns, scheme smuggling and
// structural tricks from the fixed hostile sample set.
func TestSanitizeHTMLHostileCorpus(t *testing.T) {
	hostile := []struct {
		name   string
		in     string
		banned []string
	}{
		{"script tag", "<p>hi</p><script>alert(1)</script>", []string{"script", "alert"}},
		{"uppercase script", "<SCRIPT src=\"x.js\"></SCRIPT>", []string{"script"}},
		{"nested fake script", "<scr<script>ipt>alert(1)</scr</script>ipt>", []string{"<script"}},
		{"event handler", "<img src=\"x.png\" onerror=\"alert(1)\">", []string{"onerror"}},
		{"attribute case", "<IMG SRC=\"javascript:alert(1)\">", []string{"javascript:"}},
		{"js entity scheme", "<a href=\"jav&#x09;ascript:alert(1)\">x</a>", []string{"javascript:"}},
		{"js newline scheme", "<a href=\"jav\tascript:alert(1)\">x</a>", []string{"ascript:"}},
		{"vbscript", "<a href=\"vbscript:msgbox(1)\">x</a>", []string{"vbscript:"}},
		{"data html uri", "<a href=\"data:text/html;base64,PHNjcmlwdD4=\">x</a>", []string{"data:text/html"}},
		{"svg with script", "<svg><script>alert(1)</script></svg>", []string{"svg", "script"}},
		{"svg foreignObject", "<svg><foreignObject><body>x</body></foreignObject></svg>", []string{"foreignobject"}},
		{"math mXSS", "<math><mtext><script>alert(1)</script></mtext></math>", []string{"script"}},
		{"noscript mXSS", "<noscript><p title=\"</noscript><img src=x onerror=alert(1)>\">", []string{"noscript", "onerror"}},
		{"iframe", "<iframe src=\"https://evil.example\"></iframe>", []string{"iframe"}},
		{"object data js", "<object data=\"javascript:alert(1)\"></object>", []string{"object"}},
		{"embed", "<embed src=\"https://evil.example/x.swf\">", []string{"embed"}},
		{"base href", "<base href=\"https://evil.example/\">", []string{"base"}},
		{"meta refresh", "<meta http-equiv=\"refresh\" content=\"0;url=https://evil.example\">", []string{"http-equiv"}},
		{"form action js", "<form action=\"javascript:alert(1)\"><input type=submit></form>", []string{"form", "javascript:"}},
		{"style import", "<style>@import url(https://evil.example/x.css);</style>", []string{"@import"}},
		{"style attr url", "<div style=\"background:url(https://evil.example/x.png)\">x</div>", []string{"url("}},
		{"style attr expression", "<div style=\"width:expression(alert(1))\">x</div>", []string{"expression("}},
		{"data svg img", "<img src=\"data:image/svg+xml;base64,PHN2Zz4=\">", []string{"image/svg"}},
		{"srcset tracking", "<img src=\"a.png\" srcset=\"https://track.example/x 2x\">", []string{"srcset"}},
		{"background attr", "<body background=\"https://track.example/x.png\">x</body>", []string{"background"}},
		{"protocol relative", "<a href=\"//evil.example/x\">x</a>", []string{"//evil.example"}},
		{"anchor clobber id", "<a id=\"location\" href=\"https://x.example\">x</a>", []string{"id="}},
	}
	for _, tc := range hostile {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := mustSanitize(t, tc.in)
			lower := strings.ToLower(out)
			for _, banned := range tc.banned {
				if strings.Contains(lower, strings.ToLower(banned)) {
					t.Fatalf("output contains %q:\n%s", banned, out)
				}
			}
		})
	}
}

func TestSanitizeHTMLAllowsBenignMail(t *testing.T) {
	in := "<p style=\"color:red;font-weight:bold\">Hello <b>world</b></p>" +
		"<table border=\"1\"><tr><td colspan=\"2\">cell</td></tr></table>" +
		"<img src=\"cid:part1.abc@example\" alt=\"inline\">" +
		"<a href=\"https://example.com/page\">link</a>"
	out, notes := mustSanitize(t, in)
	if !strings.Contains(out, "Hello") || !strings.Contains(out, "<b>world</b>") {
		t.Fatalf("benign content lost: %s", out)
	}
	if !strings.Contains(out, "<td colspan=\"2\"") {
		t.Fatalf("table attrs lost: %s", out)
	}
	// Anchors are hardened.
	if !strings.Contains(out, "rel=\"noopener noreferrer\"") || !strings.Contains(out, "target=\"_blank\"") {
		t.Fatalf("anchor not hardened: %s", out)
	}
	// cid images are dropped (no rewriter in this pass) with a note.
	if strings.Contains(out, "cid:") {
		t.Fatalf("cid leaked: %s", out)
	}
	if !hasNote(notes, NoteCIDUnresolved) {
		t.Fatalf("want %s note, got %v", NoteCIDUnresolved, notes)
	}
	if hasNote(notes, NoteActiveContentRemoved) {
		t.Fatalf("benign mail flagged as active content: %v", notes)
	}
}

func hasNote(notes []string, want string) bool {
	for _, n := range notes {
		if n == want {
			return true
		}
	}
	return false
}

func TestSanitizeHTMLRemoteImagesBlockedByDefaultAndAllowedOnOptIn(t *testing.T) {
	in := "<p>x</p><img src=\"https://track.example/pixel.gif\">" +
		"<img src=\"data:image/png;base64,iVBORw0KGgo=\" alt=\"dot\">"
	out, notes := mustSanitize(t, in)
	if strings.Contains(out, "https://track.example") {
		t.Fatalf("remote image survived default pass: %s", out)
	}
	if !strings.Contains(out, "data:image/png") {
		t.Fatalf("safe data image dropped: %s", out)
	}
	if !hasNote(notes, NoteRemoteImagesBlocked) {
		t.Fatalf("want remote_images_blocked note, got %v", notes)
	}
	allowed, _ := SanitizeHTMLOpts(in, SanitizeOptions{AllowRemoteImages: true})
	if !strings.Contains(allowed, "https://track.example/pixel.gif") {
		t.Fatalf("opt-in remote image dropped: %s", allowed)
	}
}

func TestSanitizeHTMLCIDRewrite(t *testing.T) {
	in := "<img src=\"cid:part1.abc@example\" alt=\"logo\">"
	rewriter := func(cid string) (string, bool) {
		if cid == "part1.abc@example" {
			return "/api/v1/mailbox/folders/INBOX/messages/7/attachments/1.2", true
		}
		return "", false
	}
	out, _ := SanitizeHTMLOpts(in, SanitizeOptions{CIDRewriter: rewriter})
	if !strings.Contains(out, "/api/v1/mailbox/folders/INBOX/messages/7/attachments/1.2") {
		t.Fatalf("cid not rewritten: %s", out)
	}
	_, notes := SanitizeHTMLOpts("<img src=\"cid:missing@example\">", SanitizeOptions{CIDRewriter: rewriter})
	if !hasNote(notes, NoteCIDUnresolved) {
		t.Fatalf("unresolved cid should note: %v", notes)
	}
}

func TestSanitizeHTMLTruncationLimit(t *testing.T) {
	big := "<p>" + strings.Repeat("x", MaxHTMLBytes+16) + "</p>"
	out, notes := SanitizeHTML(big)
	if !hasNote(notes, NoteHTMLTruncated) {
		t.Fatalf("want truncation note, got %v", notes)
	}
	if len(out) > MaxHTMLBytes+1024 {
		t.Fatalf("output not bounded: %d", len(out))
	}
}

func TestParseMIMEBodyStrictAttachmentCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("Content-Type: multipart/mixed; boundary=BOUND\r\n\r\n")
	b.WriteString("--BOUND\r\nContent-Type: text/plain\r\n\r\nbody\r\n")
	for i := 0; i < MaxAttachmentsPerMessage+5; i++ {
		b.WriteString("--BOUND\r\n")
		b.WriteString("Content-Type: application/octet-stream\r\n")
		b.WriteString("Content-Disposition: attachment; filename=\"f" + itoa(i) + "\"\r\n\r\n")
		b.WriteString("data\r\n")
	}
	b.WriteString("--BOUND--\r\n")
	_, _, atts, err := parseMIMEBodyStrict([]byte(b.String()))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(atts) != MaxAttachmentsPerMessage {
		t.Fatalf("attachments not capped: %d", len(atts))
	}
}

// itoa avoids fmt just for tiny indices in fixtures.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}

func TestExtractMIMEPartBoundedByDownloadCap(t *testing.T) {
	huge := strings.Repeat("A", MaxAttachmentBytes+1)
	var b strings.Builder
	b.WriteString("Content-Type: multipart/mixed; boundary=BOUND\r\n\r\n")
	b.WriteString("--BOUND\r\nContent-Type: application/octet-stream\r\n")
	b.WriteString("Content-Disposition: attachment; filename=\"big.bin\"\r\n\r\n")
	b.WriteString(huge + "\r\n--BOUND--\r\n")
	_, _, data, err := extractMIMEPart([]byte(b.String()), "1")
	if !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("want ErrAttachmentTooLarge, got %v (%d bytes)", err, len(data))
	}
	if data != nil {
		t.Fatalf("oversized part must not be returned")
	}
}

// An allowed remote image is rewritten to the same-origin proxy URL, so
// the reader's browser never contacts the sender's host.
func TestSanitizeHTMLRewritesRemoteImageToProxy(t *testing.T) {
	in := `<p>hi</p><img src="https://track.example/pixel.gif" alt="pixel">`
	var seen []string
	out, _ := SanitizeHTMLOpts(in, SanitizeOptions{
		AllowRemoteImages: true,
		RemoteImageProxy: func(remote string) (string, bool) {
			seen = append(seen, remote)
			return "/api/v1/mailbox/images/proxy?u=token&sig=sig", true
		},
	})
	if !strings.Contains(out, "/api/v1/mailbox/images/proxy?u=token&amp;sig=sig") {
		t.Fatalf("remote image not proxied: %s", out)
	}
	if strings.Contains(out, "track.example") {
		t.Fatalf("third-party host survived: %s", out)
	}
	if len(seen) != 1 || seen[0] != "https://track.example/pixel.gif" {
		t.Fatalf("proxy hook saw %v", seen)
	}
}

// A proxy that cannot sign drops the image: there is no direct-URL
// fallback, because a direct URL would leak the reader's address.
func TestSanitizeHTMLDropsImageWhenProxyRefuses(t *testing.T) {
	in := `<img src="https://track.example/pixel.gif" alt="pixel">`
	out, notes := SanitizeHTMLOpts(in, SanitizeOptions{
		AllowRemoteImages: true,
		RemoteImageProxy: func(string) (string, bool) {
			return "", false
		},
	})
	if strings.Contains(out, "track.example") {
		t.Fatalf("image survived a refusing proxy: %s", out)
	}
	if !hasNote(notes, NoteRemoteImagesBlocked) {
		t.Fatalf("want remote_images_blocked note, got %v", notes)
	}
}

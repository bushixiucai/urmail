package archive

import (
	"strings"
	"testing"
)

func TestSanitizerRemovesActiveContentTrackingAndAttributes(t *testing.T) {
	input := []byte(`<html><head><base href="https://tracking.invalid/"></head><body><p onclick="evil()" style="background:url(https://tracking.invalid/pixel)">Safe <strong>format</strong></p><img src="https://tracking.invalid/pixel"><svg onload="evil()"><script>evil()</script></svg><form action="https://tracking.invalid"><input value="secret"></form><iframe src="https://tracking.invalid"></iframe><a href="javascript:evil()">link text</a></body></html>`)
	safe := SanitizeHTML(input)
	for _, forbidden := range []string{"tracking.invalid", "onclick", "onload", "href=", "src=", "<script", "<img", "<iframe", "<svg", "<form", "<input", "style="} {
		if strings.Contains(safe, forbidden) {
			t.Fatalf("active content survived %q: %s", forbidden, safe)
		}
	}
	if !strings.Contains(safe, "<strong>format</strong>") || !strings.Contains(safe, "link text") {
		t.Fatalf("safe formatting lost: %s", safe)
	}
	content := ExtractMIME([]byte("Content-Type: text/html; charset=utf-8\r\n\r\n<p>Only HTML 正文</p><script>tracking()</script>"))
	if !strings.Contains(content.Body, "Only HTML 正文") || strings.Contains(content.Body, "tracking") || content.BodyHTML == "" {
		t.Fatalf("HTML search/preview extraction wrong: %+v", content)
	}
}

func TestAttachmentDecodingAndOwnershipPaths(t *testing.T) {
	raw := []byte("Content-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\nContent-Type: text/plain\r\nContent-Disposition: attachment; filename=\"../../evil.txt\"\r\nContent-Transfer-Encoding: base64\r\n\r\naGVsbG8=\r\n--x--\r\n")
	part, data, err := ExtractAttachment(raw, 0)
	if err != nil || string(data) != "hello" || part.Size != 5 {
		t.Fatalf("attachment mismatch: %+v %q %v", part, data, err)
	}
	if _, _, err = ExtractAttachment(raw, 1); err == nil {
		t.Fatal("unknown attachment accepted")
	}
	if ValidateOwnershipPath("/archive", "/archive/2/3/file.eml", 1, 3) == nil {
		t.Fatal("foreign tenant original path accepted")
	}
	if ValidateOwnershipPath("/archive", "/archive/1/4/file.eml", 1, 3) == nil {
		t.Fatal("foreign mailbox original path accepted")
	}
	if err = ValidateOwnershipPath("/archive", "/archive/1/3/file.eml", 1, 3); err != nil {
		t.Fatal(err)
	}
}

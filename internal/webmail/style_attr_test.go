package webmail

import (
	"strings"
	"testing"
)

func proxyStub(path string) func(string) (string, bool) {
	return func(_ string) (string, bool) {
		return "/img-proxy?u=" + path, true
	}
}

// TestSplitStyleDeclarations pins the tokenizer contract: semicolons
// inside quotes or parens do not split, unbalanced input refuses.
func TestSplitStyleDeclarations(t *testing.T) {
	decls, ok := splitStyleDeclarations(`color:red;font-family:"a;b";background:url(x/y.png) no-repeat`)
	if !ok || len(decls) != 3 {
		t.Fatalf("ok=%v decls=%q", ok, decls)
	}
	if _, ok := splitStyleDeclarations(`color:red;width:calc(100% `); ok {
		t.Fatalf("unbalanced parens accepted")
	}
	if _, ok := splitStyleDeclarations(`font-family:"unterminated`); ok {
		t.Fatalf("unterminated quote accepted")
	}
}

// TestSanitizeStyleAttrDeclarationLevel pins the PR-5 contract: safe
// declarations survive verbatim, a background url() rides the img proxy
// policy, unsafe non-background declarations drop alone.
func TestSanitizeStyleAttrDeclarationLevel(t *testing.T) {
	opts := SanitizeOptions{AllowRemoteImages: true, RemoteImageProxy: proxyStub("p1")}
	out, dropped, notes := sanitizeStyleAttr(`color:red;background-image:url(https://cdn.example.com/pic.png);font-size:14px`, opts)
	if dropped || len(notes) != 0 {
		t.Fatalf("unexpected notes: %v", notes)
	}
	if !strings.Contains(out, "color:red") || !strings.Contains(out, "font-size:14px") {
		t.Fatalf("safe declarations lost: %s", out)
	}
	if !strings.Contains(out, `background-image: url("/img-proxy?u=p1")`) {
		t.Fatalf("background not proxied: %s", out)
	}
	if strings.Contains(out, "cdn.example.com") {
		t.Fatalf("remote URL leaked: %s", out)
	}
}

func TestSanitizeStyleAttrBlockedRemote(t *testing.T) {
	out, dropped, notes := sanitizeStyleAttr(`background-image:url(https://cdn.example.com/pic.png);color:red`, SanitizeOptions{})
	if dropped || !strings.Contains(out, "color:red") {
		t.Fatalf("attribute wrongly dropped: %q %v", out, notes)
	}
	if strings.Contains(out, "background") || strings.Contains(out, "cdn.example.com") {
		t.Fatalf("blocked background kept: %s", out)
	}
	if !strings.Contains(out, "color:red") {
		t.Fatalf("safe declaration lost: %s", out)
	}
	joined := strings.Join(notes, ",")
	if !strings.Contains(joined, NoteRemoteImagesBlocked) {
		t.Fatalf("remote block not reported: %v", notes)
	}
}

func TestSanitizeStyleAttrUnsafeOutsideBackground(t *testing.T) {
	opts := SanitizeOptions{AllowRemoteImages: true, RemoteImageProxy: proxyStub("p")}
	out, dropped, notes := sanitizeStyleAttr(`width:expression(alert(1));color:blue;list-style-image:url(https://x.example/dot.gif)`, opts)
	if dropped || strings.Contains(out, "expression") || strings.Contains(out, "list-style") || strings.Contains(out, "x.example") {
		t.Fatalf("unsafe declarations kept: %s", out)
	}
	if !strings.Contains(out, "color:blue") {
		t.Fatalf("safe declaration lost: %s", out)
	}
	if len(notes) != 1 || notes[0] != NoteUnsafeStyleRemoved {
		t.Fatalf("notes: %v", notes)
	}
}

// TestSanitizeStyleAttrVerbatim keeps byte-identical output for CSS with
// no unsafe construct, so corpus goldens stay stable.
func TestSanitizeStyleAttrVerbatim(t *testing.T) {
	val := `color:red; margin: 0 auto; padding-left: 8px`
	out, dropped, notes := sanitizeStyleAttr(val, SanitizeOptions{})
	if dropped || out != val || notes != nil {
		t.Fatalf("out=%q notes=%v", out, notes)
	}
}

// TestSanitizeStyleAttrBackgroundShorthandRewritesOnlyTheURL keeps the
// shorthand's positional values while the reference goes through the proxy.
func TestSanitizeStyleAttrBackgroundShorthandRewritesOnlyTheURL(t *testing.T) {
	opts := SanitizeOptions{AllowRemoteImages: true, RemoteImageProxy: proxyStub("bg")}
	out, dropped, notes := sanitizeStyleAttr(`background:url("https://a.example/bg.jpg") no-repeat center`, opts)
	if dropped || len(notes) != 0 {
		t.Fatalf("notes: %v", notes)
	}
	if !strings.Contains(out, `no-repeat center`) || strings.Contains(out, "a.example") {
		t.Fatalf("shorthand mangled: %s", out)
	}
}

func TestSanitizeStyleAttrDataURIKept(t *testing.T) {
	uri := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUg=="
	out, dropped, notes := sanitizeStyleAttr(`background-image:url(`+uri+`);color:red`, SanitizeOptions{})
	if dropped || len(notes) != 0 || !strings.Contains(out, uri) {
		t.Fatalf("safe data URI lost: %q %v", out, notes)
	}
}

// TestSanitizeStyleAttrDroppedWhenNothingSurvives drops the whole
// attribute when every declaration is refused.
func TestSanitizeStyleAttrDroppedWhenNothingSurvives(t *testing.T) {
	out, dropped, notes := sanitizeStyleAttr(`width:expression(alert(1))`, SanitizeOptions{})
	if !dropped || out != "" || len(notes) != 1 || notes[0] != NoteUnsafeStyleRemoved {
		t.Fatalf("out=%q notes=%v", out, notes)
	}
}

// TestSanitizeHTMLKeepsFontAttributes pins the legacy <font> fix: color,
// face and size survive sanitization.
func TestSanitizeHTMLKeepsFontAttributes(t *testing.T) {
	out, notes := SanitizeHTML(`<p><font color="#ff0000" face="Arial" size="3">legacy</font></p>`)
	if len(notes) != 0 {
		t.Fatalf("notes: %v", notes)
	}
	for _, a := range []string{`color="#ff0000"`, `face="Arial"`, `size="3"`} {
		if !strings.Contains(out, a) {
			t.Fatalf("font attribute %s missing: %s", a, out)
		}
	}
}

// TestSanitizeHTMLStyleDeclarationLevel runs the full sanitizer over a
// mixed style attribute end to end.
func TestSanitizeHTMLStyleDeclarationLevel(t *testing.T) {
	opts := SanitizeOptions{AllowRemoteImages: true, RemoteImageProxy: proxyStub("e2e")}
	out, notes := SanitizeHTMLOpts(`<div style="background:url(https://b.example/i.png) repeat;color:#333;width:expression(x)">hi</div>`, opts)
	if !strings.Contains(out, `background: url(&#34;/img-proxy?u=e2e&#34;) repeat`) {
		t.Fatalf("background shorthand not proxied: %s", out)
	}
	if strings.Contains(out, "expression") || strings.Contains(out, "b.example") {
		t.Fatalf("unsafe construct kept: %s", out)
	}
	if !strings.Contains(out, "color:#333") {
		t.Fatalf("safe declaration lost: %s", out)
	}
	if len(notes) != 1 || notes[0] != NoteUnsafeStyleRemoved {
		t.Fatalf("notes: %v", notes)
	}
}

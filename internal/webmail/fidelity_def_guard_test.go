package webmail

import (
	"strings"
	"testing"
)

// V0620-FIDELITY-REVIEW DEF-7: one undecodable encoded-word (broken bytes
// inside a gb2312 word) must not leak the raw encoded-word form to the UI.
func TestDecodeMIMEHeaderPerWordDegradation(t *testing.T) {
	// Legal gb2312 still decodes normally.
	if got := decodeMIMEHeader("=?gb2312?B?xOO6ww==?="); got != "你好" {
		t.Fatalf("legal gb2312 = %q", got)
	}
	// An illegal byte decodes to a replacement character, not a raw word.
	if got := decodeMIMEHeader("=?gb2312?B?/w==?="); strings.Contains(got, "=?") {
		t.Fatalf("illegal word leaked raw form: %q", got)
	}
	// Literal text around a broken word survives.
	if got := decodeMIMEHeader("Alice =?gb2312?B?/w==?= Bob"); got != "Alice � Bob" {
		t.Fatalf("mixed literal = %q", got)
	}
	// A good word next to a broken one still decodes.
	if got := decodeMIMEHeader("=?utf-8?B?5oiR?==?gb2312?B?/w==?="); got != "我�" {
		t.Fatalf("good word next to broken one = %q", got)
	}
	// Plain text passes through untouched.
	if got := decodeMIMEHeader("Plain � Name"); got != "Plain � Name" {
		t.Fatalf("plain = %q", got)
	}
}

// V0620-FIDELITY-REVIEW DEF-3 server-side guard: dark mail backgrounds must
// survive the render pipeline in all three authoring forms. The visual
// unreadability UIUX saw traces to the client-side white bucket being
// missing (mail-body-bucket), not to background stripping here.
func TestDarkBackgroundsSurviveRender(t *testing.T) {
	// 1) background-color in an inline style attribute.
	doc := `<div style="background-color:#141414;color:#f5f5f5"><p>dark body</p></div>`
	inlined := InlineCSS(doc, UAClassDesktop, ColorSchemeNone)
	safe, _ := SanitizeHTMLOpts(inlined, SanitizeOptions{})
	wrapped := strings.ToLower(WrapHTMLDocument(AdaptHTMLForViewport(safe, Viewport{UAClass: UAClassDesktop, Width: 1280})))
	if !strings.Contains(wrapped, "#141414") {
		t.Fatalf("style-attr background stripped: %s", wrapped)
	}
	if !strings.Contains(wrapped, "#f5f5f5") {
		t.Fatalf("foreground color stripped: %s", wrapped)
	}

	// 2) the legacy bgcolor attribute.
	doc2 := `<table><tr><td bgcolor="#141414"><p style="color:#f5f5f5">x</p></td></tr></table>`
	inlined2 := InlineCSS(doc2, UAClassDesktop, ColorSchemeNone)
	safe2, _ := SanitizeHTMLOpts(inlined2, SanitizeOptions{})
	wrapped2 := strings.ToLower(WrapHTMLDocument(AdaptHTMLForViewport(safe2, Viewport{UAClass: UAClassDesktop, Width: 1280})))
	if !strings.Contains(wrapped2, `bgcolor="#141414"`) && !strings.Contains(wrapped2, "#141414") {
		t.Fatalf("bgcolor stripped: %s", wrapped2)
	}

	// 3) the mail's own prefers-color-scheme:dark block under the M1 dark
	// bucket: the rules inline server-side and keep their backgrounds.
	doc3 := `<style>@media (prefers-color-scheme:dark){.wrap{background-color:#141414;color:#f5f5f5}}</style><div class="wrap">x</div>`
	inlined3 := InlineCSS(doc3, UAClassDesktop, ColorSchemeDark)
	safe3, _ := SanitizeHTMLOpts(inlined3, SanitizeOptions{})
	wrapped3 := strings.ToLower(WrapHTMLDocument(AdaptHTMLForViewport(safe3, Viewport{
		UAClass: UAClassDesktop, Width: 1280, ColorScheme: ColorSchemeDark,
	})))
	if !strings.Contains(wrapped3, "#141414") {
		t.Fatalf("dark-bucket media background not inlined: %s", wrapped3)
	}
}

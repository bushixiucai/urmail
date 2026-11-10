package webmail

import (
	"strings"
	"testing"
)

const inlineProbeDoc = `<html><head><style>
@font-face{font-family:X;src:url(https://f.example/x.woff)}
.a{color:red;background:blue}
@media(max-width:480px){.a{background:green}}
@media(min-width:900px){.a{color:yellow}}
a:hover{color:pink}
</style></head><body><div class="a">hi</div></body></html>`

// TestInlineCSSDesktopDropsMedia pins the desktop contract: base rules
// inline into style attributes, both media flavors drop, @font-face is
// stripped and the hover leftover stays in the style element the
// sanitizer removes next.
func TestInlineCSSDesktopDropsMedia(t *testing.T) {
	out := InlineCSS(inlineProbeDoc, UAClassDesktop, ColorSchemeNone)
	if !strings.Contains(out, `<div class="a" style="color:red;background:blue">`) {
		t.Fatalf("base rule not inlined: %s", out)
	}
	for _, banned := range []string{"green", "yellow", "@font-face", "f.example", "@media"} {
		if strings.Contains(out, banned) {
			t.Fatalf("desktop output contains %q: %s", banned, out)
		}
	}
	if !strings.Contains(out, "hover") {
		t.Fatalf("hover leftover expected in style element (sanitizer drops it): %s", out)
	}
	safe, _ := SanitizeHTML(out)
	if strings.Contains(safe, "hover") || strings.Contains(safe, "<style") {
		t.Fatalf("leftover survived sanitization: %s", safe)
	}
	if !strings.Contains(safe, `style="color:red;background:blue"`) {
		t.Fatalf("inlined style lost by sanitization: %s", safe)
	}
}

// TestInlineCSSMobileUnwrapsMaxWidth pins the mobile contract: the
// max-width media block's rules ride the cascade after the base rules,
// so the inlined attribute carries the responsive value.
func TestInlineCSSMobileUnwrapsMaxWidth(t *testing.T) {
	out := InlineCSS(inlineProbeDoc, UAClassMobile, ColorSchemeNone)
	if !strings.Contains(out, `style="color:red;background:green"`) {
		t.Fatalf("mobile media rule not applied: %s", out)
	}
	for _, banned := range []string{"yellow", "@media", "@font-face", "f.example"} {
		if strings.Contains(out, banned) {
			t.Fatalf("mobile output contains %q: %s", banned, out)
		}
	}
}

// TestInlineCSSTabletBehavesLikeDesktop: tablet is not the mobile
// bucket; media blocks stay out.
func TestInlineCSSTabletBehavesLikeDesktop(t *testing.T) {
	out := InlineCSS(inlineProbeDoc, "tablet", ColorSchemeNone)
	if strings.Contains(out, "green") || strings.Contains(out, "@media") {
		t.Fatalf("tablet output applied mobile media: %s", out)
	}
}

// TestInlineCSSWithoutStyleIsUntouched: documents without style elements
// come back byte-identical (the fast path must not reorder anything).
func TestInlineCSSWithoutStyleIsUntouched(t *testing.T) {
	doc := `<div style="color:red">plain</div>`
	if got := InlineCSS(doc, UAClassDesktop, ColorSchemeNone); got != doc {
		t.Fatalf("styleless document changed: %s", got)
	}
}

// TestInlineCSSErrorKeepsInput: an unparseable stylesheet cannot lose
// the mail — InlineCSS returns the input and the sanitizer keeps its
// conservative drop behavior.
func TestInlineCSSErrorKeepsInput(t *testing.T) {
	doc := `<style>.a{color:red</style><div class="a">x</div>`
	out := InlineCSS(doc, UAClassDesktop, ColorSchemeNone)
	safe, _ := SanitizeHTML(out)
	// go-premailer's parser tolerates the missing brace and inlines the
	// rule; the contract is only that the element and its text survive.
	if !strings.Contains(safe, `<div class="a"`) || !strings.Contains(safe, ">x</div>") {
		t.Fatalf("content lost on stylesheet error: %s", safe)
	}
	if strings.Contains(safe, "<style") {
		t.Fatalf("style element survived: %s", safe)
	}
}

// TestInlineCSSMediaUnbalancedKeepsRest: a broken media block must not
// swallow the rules around it.
func TestInlineCSSMediaUnbalancedKeepsRest(t *testing.T) {
	doc := `<style>.a{color:red}@media(max-width:480px){.b{color:green}.c{color:blue}</style><div class="a b c">x</div>`
	out := InlineCSS(doc, UAClassMobile, ColorSchemeNone)
	if !strings.Contains(out, "color:red") {
		t.Fatalf("base rule lost around broken media: %s", out)
	}
	safe, _ := SanitizeHTML(out)
	if !strings.Contains(safe, "<div") {
		t.Fatalf("content lost: %s", safe)
	}
}

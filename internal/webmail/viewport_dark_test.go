package webmail

import (
	"strings"
	"testing"
)

// TestAdaptHTMLDeclaresColorScheme pins the dark-scheme contract: the
// render fragment declares support for light and dark at its head and the
// wrapper layer follows the user preference under the media query. No rule
// may recolor email content — dark support is a scheme declaration only
// (Gmail-style restraint), so marketers' own dark-themed mails keep their
// design and light mails keep white backgrounds.
func TestAdaptHTMLDeclaresColorScheme(t *testing.T) {
	out := AdaptHTMLForViewport("<p>hello</p>", Viewport{Width: 375, UAClass: "mobile"})
	if !strings.Contains(out, `<meta name="color-scheme" content="light dark">`) {
		t.Fatalf("color-scheme meta missing: %s", out[:200])
	}
	if strings.Index(out, `<meta name="color-scheme"`) != 0 {
		t.Fatalf("meta must ride at the fragment head: %s", out[:120])
	}
	if !strings.Contains(out, `@media (prefers-color-scheme:dark){.urmail-vp{color-scheme:light dark}}`) {
		t.Fatalf("wrapper prefers-color-scheme rule missing")
	}
	// Restraint: the dark block must not assign background or color values.
	head := strings.Index(out, "prefers-color-scheme")
	if head < 0 {
		t.Fatalf("prefers-color-scheme rule missing: %s", out)
	}
	dark := out[head:]
	if strings.Contains(dark, "background") || strings.Contains(dark, "color:#") || strings.Contains(dark, "color: #") {
		t.Fatalf("dark block recolors content: %s", dark)
	}
}

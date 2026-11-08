package webmail

import (
	"strings"
	"testing"
)

// TestAdaptHTMLClampsWideInsetsOnNarrow pins the narrow-pane gutter clamp:
// desktop gutters of 48-50px cost a quarter of a 375px pane and leave a
// large blank band beside the text (the "97px of slack" production
// report). Oversized horizontal insets clamp to a mobile-typical gutter;
// centering auto values, small insets and wider buckets stay untouched.
func TestAdaptHTMLClampsWideInsetsOnNarrow(t *testing.T) {
	in := `<div style="margin-left:50px;margin-right:50px;padding-left:48px;padding-right:10px;margin-top:40px"><p>x</p></div>` +
		`<table style="margin:0 auto"><tr><td>a</td></tr></table>`

	narrow := AdaptHTMLForViewport(in, Viewport{UAClass: "mobile", Width: 375})
	for _, banned := range []string{"margin-left:50px", "margin-right:50px", "padding-left:48px"} {
		if strings.Contains(narrow, banned) {
			t.Fatalf("narrow output keeps oversized inset %s: %s", banned, narrow)
		}
	}
	if !strings.Contains(narrow, "margin-left:16px") || !strings.Contains(narrow, "margin-right:16px") {
		t.Fatalf("narrow output missing the clamped gutters: %s", narrow)
	}
	if !strings.Contains(narrow, "padding-left:16px") {
		t.Fatalf("narrow output missing the clamped padding: %s", narrow)
	}
	// Small insets, vertical insets and centering stay as designed.
	if !strings.Contains(narrow, "padding-right:10px") {
		t.Fatalf("small inset was clamped too: %s", narrow)
	}
	if !strings.Contains(narrow, "margin-top:40px") {
		t.Fatalf("vertical inset was clamped: %s", narrow)
	}
	if !strings.Contains(narrow, "margin:0 auto") {
		t.Fatalf("centering auto was rewritten: %s", narrow)
	}

	// Desktop keeps the design gutters.
	wide := AdaptHTMLForViewport(in, Viewport{UAClass: "desktop", Width: 1280})
	if !strings.Contains(wide, "margin-left:50px") || !strings.Contains(wide, "padding-left:48px") {
		t.Fatalf("desktop output lost the design gutters: %s", wide)
	}
}

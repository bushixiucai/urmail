package webmail

import (
	"strings"
	"testing"
)

func TestParseUAClassAndViewportBucket(t *testing.T) {
	if ParseUAClass("MOBILE") != UAClassMobile {
		t.Fatal("mobile")
	}
	if ParseUAClass("tablet") != UAClassTablet {
		t.Fatal("tablet")
	}
	if ParseUAClass("") != UAClassDesktop {
		t.Fatal("default desktop")
	}
	if ViewportBucket(375) != "narrow" || ViewportBucket(800) != "medium" || ViewportBucket(1200) != "wide" {
		t.Fatal("buckets")
	}
	if ParseViewportWidth("-1") != 0 || ParseViewportWidth("abc") != 0 {
		t.Fatal("bad width")
	}
}

func TestAdaptHTMLRewritesFixedWidths(t *testing.T) {
	in := `<table width="600" style="width:600px"><tr><td width="600"><div style="width: 600px; color:red">hello</div></td></tr></table>`
	out := AdaptHTMLForViewport(in, Viewport{UAClass: "mobile", Width: 375})
	if strings.Contains(out, `width="600"`) {
		t.Fatalf("attr width survived: %s", out)
	}
	if strings.Contains(strings.ToLower(out), "width:600px") {
		t.Fatalf("style width survived: %s", out)
	}
	if !strings.Contains(out, "max-width:100%") || !strings.Contains(out, "width:100%") {
		t.Fatalf("fluid style missing: %s", out)
	}
	if !strings.Contains(out, "urmail-vp-narrow") || !strings.Contains(out, "urmail-ua-mobile") {
		t.Fatalf("breakpoint class missing: %s", out)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("content lost: %s", out)
	}
}

func TestAdaptHTMLLeavesSmallWidths(t *testing.T) {
	in := `<img width="16" style="width:16px" alt="x">`
	out := AdaptHTMLForViewport(in, Viewport{Width: 1200})
	if !strings.Contains(out, `width="16"`) {
		t.Fatalf("small attr width rewritten: %s", out)
	}
	if !strings.Contains(out, "width:16px") {
		t.Fatalf("small style width rewritten: %s", out)
	}
	if !strings.Contains(out, "urmail-vp-wide") {
		t.Fatalf("wide bucket missing: %s", out)
	}
}

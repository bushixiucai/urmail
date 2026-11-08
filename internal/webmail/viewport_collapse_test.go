package webmail

import (
	"strings"
	"testing"
)

// TestAdaptHTMLKeepsColumnProportions pins the multi-column contract: the
// cells of a full-width row keep their relative sizes as percentages of the
// row total instead of each claiming width:100%. Forcing every cell to full
// width collapsed multi-column mail into one crushed column with vertical
// text (production defect v0.6.10-hotfix-2).
func TestAdaptHTMLKeepsColumnProportions(t *testing.T) {
	in := `<table width="600"><tr><td width="420">left</td><td width="180">right</td></tr></table>`
	out := AdaptHTMLForViewport(in, Viewport{UAClass: "mobile", Width: 375})
	if !strings.Contains(out, `width="70%"`) || !strings.Contains(out, `width="30%"`) {
		t.Fatalf("column shares lost: %s", out)
	}
	if strings.Count(out, `width="100%"`) != 1 { // only the table is fluid
		t.Fatalf("a cell became full width: %s", out)
	}
}

// TestAdaptHTMLCellStyleWidthsPercentified covers columns declared in inline
// styles instead of width attributes.
func TestAdaptHTMLCellStyleWidthsPercentified(t *testing.T) {
	in := `<table><tr><td style="width: 400px; padding:0">A</td><td style="width: 200px">B</td></tr></table>`
	out := AdaptHTMLForViewport(in, Viewport{UAClass: "mobile", Width: 375})
	if !strings.Contains(out, "width:66.67%") || !strings.Contains(out, "width:33.33%") {
		t.Fatalf("style column shares lost: %s", out)
	}
	if strings.Contains(out, "width:400px") || strings.Contains(out, "width:200px") {
		t.Fatalf("pixel column width survived: %s", out)
	}
}

// TestAdaptHTMLSingleColumnStaysFull keeps the approved behavior for
// single-column rows: the lone cell fills the reading pane.
func TestAdaptHTMLSingleColumnStaysFull(t *testing.T) {
	in := `<table width="600"><tr><td width="560">body</td></tr></table>`
	out := AdaptHTMLForViewport(in, Viewport{UAClass: "mobile", Width: 375})
	if !strings.Contains(out, `<td width="100%"`) {
		t.Fatalf("single column did not become fluid: %s", out)
	}
}

// TestAdaptHTMLSmallRowKeepsDesignedSizes refuses to rescale rows that fit
// the narrowest pane anyway: stretching a 36px icon cell to a pane share
// would distort the design far more than the fixed width ever clipped.
func TestAdaptHTMLSmallRowKeepsDesignedSizes(t *testing.T) {
	in := `<table><tr><td width="150">a</td><td width="150">b</td></tr></table>`
	out := AdaptHTMLForViewport(in, Viewport{UAClass: "mobile", Width: 375})
	if !strings.Contains(out, `width="150"`) {
		t.Fatalf("small row was rescaled: %s", out)
	}
	if !strings.Contains(out, "max-width:100%") {
		t.Fatalf("cells lost the pane cap: %s", out)
	}
}

// TestAdaptHTMLMixedRowKeepsDesign pins the fallback: a row mixing measured
// cells with auto cells cannot be rescaled without distorting the auto
// column, so it keeps its markup and is only capped.
func TestAdaptHTMLMixedRowKeepsDesign(t *testing.T) {
	in := `<table width="600"><tr><td width="420">fixed</td><td>auto</td></tr></table>`
	out := AdaptHTMLForViewport(in, Viewport{UAClass: "mobile", Width: 375})
	if !strings.Contains(out, `width="420"`) {
		t.Fatalf("mixed row was rescaled: %s", out)
	}
	if strings.Contains(out, `<td width="100%"`) {
		t.Fatalf("mixed row produced a full-width cell: %s", out)
	}
	if !strings.Contains(out, "max-width:100%") {
		t.Fatalf("cells lost the pane cap: %s", out)
	}
}

// TestAdaptHTMLSpanningRowUntouched keeps colspan rows out of the rescaling
// path: a spanning cell crosses columns, so row-local percentages would
// misplace every following column.
func TestAdaptHTMLSpanningRowUntouched(t *testing.T) {
	in := `<table width="600"><tr><td colspan="2" width="560">wide</td><td width="40">x</td></tr></table>`
	out := AdaptHTMLForViewport(in, Viewport{UAClass: "mobile", Width: 375})
	if !strings.Contains(out, `width="560"`) {
		t.Fatalf("spanning row was rescaled: %s", out)
	}
}

// TestAdaptHTMLColumnGroupPercentified covers table-layout columns declared
// through <colgroup>; unrescaled pixel columns would keep overriding the
// cell widths on every pane.
func TestAdaptHTMLColumnGroupPercentified(t *testing.T) {
	in := `<table width="600"><colgroup><col width="200"><col width="400"></colgroup>` +
		`<tr><td>a</td><td>b</td></tr></table>`
	out := AdaptHTMLForViewport(in, Viewport{UAClass: "mobile", Width: 375})
	if !strings.Contains(out, `width="33.33%"`) || !strings.Contains(out, `width="66.67%"`) {
		t.Fatalf("column group shares lost: %s", out)
	}
}

// TestAdaptHTMLWideViewportKeepsColumnProportions makes sure the desktop
// reading pane renders the same proportions instead of re-collapsing.
func TestAdaptHTMLWideViewportKeepsColumnProportions(t *testing.T) {
	in := `<table width="600"><tr><td width="420">left</td><td width="180">right</td></tr></table>`
	out := AdaptHTMLForViewport(in, Viewport{UAClass: "desktop", Width: 1280})
	if !strings.Contains(out, `width="70%"`) || !strings.Contains(out, `width="30%"`) {
		t.Fatalf("column shares lost on desktop: %s", out)
	}
}

// TestAdaptHTMLBlockedImageLeavesNoShell is the sanitizer/viewport joint
// contract: a blocked remote image disappears completely instead of leaving
// a src-less element that the reading pane renders as a broken-image glyph.
func TestAdaptHTMLBlockedImageLeavesNoShell(t *testing.T) {
	in := `<table width="600"><tr><td><img src="https://a.example/banner.png" style="width:100%;height:auto" alt="banner"></td></tr></table>`
	safe, _ := SanitizeHTML(in)
	out := AdaptHTMLForViewport(safe, Viewport{UAClass: "mobile", Width: 375})
	if strings.Contains(out, "<img") {
		t.Fatalf("blocked image left a shell behind: %s", out)
	}
}

// slackLikeLayout mirrors the structure of the production sample (a trial
// expiry marketing mail): one wide content column (546px inline style), a
// feature list whose icon/gap/text cells are sized through CSS classes that
// the sanitizer removes, and a footer row with a logo cell and a right
// aligned cell. All senders, URLs and copy are replaced; only the layout
// skeleton is kept.
var slackLikeLayout = `<table id="body" class="card" style="border: 0; border-collapse: collapse; margin: 0 auto; background: white; border-radius: 8px; margin-bottom: 16px;">` +
	`<tr><td style="width: 546px; vertical-align: top; padding-top: 32px">` +
	`<div style="max-width: 600px; margin: 0 auto;">` +
	`<div style="margin-left: 50px; margin-right: 50px;" class="lg_margin">` +
	`<div style="margin-top: 18px;" class="logo_style"><img height="45" src="https://brand.example/logo.png" alt="logo"></div>` +
	`<h1>Trial ends soon</h1>` +
	`<div style="margin-bottom:20px"><table><tr>` +
	`<td class="list-item--icon_width"><img src="https://brand.example/clock.png" alt="clock icon"></td>` +
	`<td class="list-item--gap_width"></td>` +
	`<td><h3 class="list-item--header_font">Trial ends on Nov 15</h3><div class="text">Upgrade now to keep access.</div></td>` +
	`</tr></table></div>` +
	`<div style="margin-bottom:20px"><table><tr>` +
	`<td class="list-item--icon_width_large"><img src="https://brand.example/sc.png" alt="icon"></td>` +
	`<td class="list-item--gap_width_large"></td>` +
	`<td><h3 class="list-item--header_font_large">Channels</h3><div class="text">Work with external teams.</div></td>` +
	`</tr></table></div>` +
	`</div></td></tr></table>` +
	`<table style="padding: 0; margin: 0; width: 100%;"><tr>` +
	`<td class="email_footer" style="font-size: 15px;color: #717274;text-align: center;width: 100%;">footer line</td>` +
	`</tr></table>`

// TestAdaptHTMLSlackLikeLayoutStaysReadable runs the desensitized production
// structure through the pipeline at both pane widths: the wide content
// column becomes fluid, the class-sized feature cells never claim the full
// pane, the text column keeps its automatic width, and no image shell
// survives the blocked default pass.
func TestAdaptHTMLSlackLikeLayoutStaysReadable(t *testing.T) {
	for _, vp := range []Viewport{
		{UAClass: "mobile", Width: 375},
		{UAClass: "desktop", Width: 1280},
	} {
		safe, _ := SanitizeHTMLOpts(slackLikeLayout, SanitizeOptions{})
		out := AdaptHTMLForViewport(safe, vp)
		if strings.Contains(out, "<img") {
			t.Fatalf("vp %+v: image shell survived: %s", vp, out)
		}
		if !strings.Contains(out, "Trial ends on Nov 15") || !strings.Contains(out, "footer line") {
			t.Fatalf("vp %+v: content lost: %s", vp, out)
		}
		for _, forced := range []string{
			`<td class="list-item--icon_width" style="max-width:100%;width:100%"`,
			`<td class="list-item--gap_width" style="max-width:100%;width:100%"`,
			`<td style="max-width:100%;width:100%"><h3`,
		} {
			if strings.Contains(out, forced) {
				t.Fatalf("vp %+v: feature cell forced full width: %s", vp, out)
			}
		}
		if !strings.Contains(out, "width:100%") {
			t.Fatalf("vp %+v: content column did not become fluid: %s", vp, out)
		}
	}
}

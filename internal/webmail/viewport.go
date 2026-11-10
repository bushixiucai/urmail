package webmail

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Viewport describes the client's reading surface. The server uses it to
// pick a breakpoint class and to rewrite fixed widths that would otherwise
// clip marketing mail on a narrow pane.
type Viewport struct {
	// UAClass is one of desktop / mobile / tablet (empty = desktop).
	UAClass string
	// Width is the reading-pane width in CSS pixels (0 = unknown).
	Width int
	// ColorScheme is the client's explicit light/dark request (X-Color-Scheme
	// header; empty = not requested, browser preference applies).
	ColorScheme ColorScheme
	// TextScale is the client's requested reading text size (X-Text-Scale
	// header; empty = the mail's own sizing applies).
	TextScale TextScale
}

// ColorScheme buckets the explicit scheme request. When set, the render
// resolves prefers-color-scheme media queries server-side, so the cached
// output for one bucket is fully determined (contract: one evaluation per
// bucket, SSR-RENDER-PLAN M1).
type ColorScheme string

const (
	// ColorSchemeNone means the client did not opt in; user preference
	// applies.
	ColorSchemeNone ColorScheme = ""
	// ColorSchemeLight requests the light presentation.
	ColorSchemeLight ColorScheme = "light"
	// ColorSchemeDark requests the dark presentation.
	ColorSchemeDark ColorScheme = "dark"
)

// ParseColorScheme normalises the X-Color-Scheme header; unknown values
// fail safe to none (current behavior).
func ParseColorScheme(raw string) ColorScheme {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "light":
		return ColorSchemeLight
	case "dark":
		return ColorSchemeDark
	default:
		return ColorSchemeNone
	}
}

// TextScale buckets the requested reading text size.
type TextScale string

const (
	// TextScaleNone keeps the mail's own sizing.
	TextScaleNone TextScale = ""
	// TextScaleSmall reads slightly denser than the mail's base size.
	TextScaleSmall TextScale = "sm"
	// TextScaleMedium is the default and never injects sizing.
	TextScaleMedium TextScale = "md"
	// TextScaleLarge reads slightly larger than the mail's base size.
	TextScaleLarge TextScale = "lg"
)

// ParseTextScale normalises the X-Text-Scale header; unknown values fail
// safe to none.
func ParseTextScale(raw string) TextScale {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "sm", "small":
		return TextScaleSmall
	case "md", "medium":
		return TextScaleMedium
	case "lg", "large":
		return TextScaleLarge
	default:
		return TextScaleNone
	}
}

const (
	// UAClassDesktop is the default reading surface.
	UAClassDesktop = "desktop"
	// UAClassMobile is a phone-sized reading surface.
	UAClassMobile = "mobile"
	// UAClassTablet is a tablet-sized reading surface.
	UAClassTablet = "tablet"
)

// ParseUAClass normalises the X-UA-Class header.
func ParseUAClass(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case UAClassMobile:
		return UAClassMobile
	case UAClassTablet:
		return UAClassTablet
	default:
		return UAClassDesktop
	}
}

// ParseViewportWidth parses X-Viewport-Width as a positive pixel count.
func ParseViewportWidth(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 || n > 10000 {
		return 0
	}
	return n
}

// ViewportBucket maps a width onto the three breakpoints the CSS uses.
func ViewportBucket(width int) string {
	switch {
	case width > 0 && width <= 640:
		return "narrow"
	case width >= 641 && width <= 1024:
		return "medium"
	default:
		return "wide"
	}
}

var (
	widthAttrRE = regexp.MustCompile(`(?i)^\s*(\d+)\s*(px)?\s*$`)
	// widthStyleRE matches a plain pixel width declaration only. calc()
	// widths (width:calc(100% - 480px)) deliberately do not match: they
	// are already fluid, and treating them as fixed pixels would both
	// distort row rescaling and destroy the declaration.
	widthStyleRE = regexp.MustCompile(`(?i)(^|;)\s*width\s*:\s*(\d+)\s*px\s*(;|$)`)
	// maxWidthStyleRE matches a fixed-pixel max-width cap. Caps at or
	// above the fluid threshold are restated as 100% so they track the
	// reading pane instead of the design grid. Kept separate from
	// widthStyleRE on purpose: fixedPixelWidth must never read a cap as
	// a row-measurable width.
	maxWidthStyleRE = regexp.MustCompile(`(?i)(max-width\s*:\s*)(\d+)\s*px`)
	// minWidthStyleRE matches a fixed-pixel min-width declaration. The
	// separator-inclusive form allows whole-declaration removal on
	// narrow panes, where an oversized min-width forces horizontal
	// overflow.
	minWidthStyleRE = regexp.MustCompile(`(?:^|;)\s*min-width\s*:\s*(\d+)\s*px\s*(?:;|$)`)
	// insetStyleRE captures one fixed-pixel horizontal inset declaration
	// (margin or padding, left or right) inside a style attribute. The
	// declaration name is specific enough to anchor without a prefix; a
	// (^|;) prefix would consume the shared separator and hide the next
	// declaration from the same pass.
	insetStyleRE = regexp.MustCompile(`(?i)((?:margin|padding)-(?:left|right))\s*:\s*(\d+)\s*px\s*(;|$)`)
)

const (
	// narrowInsetThreshold is the horizontal inset size that a narrow
	// reading pane cannot afford: desktop designs routinely use 48-50px
	// gutters, which on a 375px pane eat over a quarter of the width.
	narrowInsetThreshold = 32
	// narrowInsetReplacement is the desktop-typical mobile gutter the
	// oversized insets clamp down to.
	narrowInsetReplacement = 16
)

// AdaptHTMLForViewport rewrites fixed widths and wraps the body in a
// breakpoint class so marketing mail adapts to the reading pane. The
// input is assumed to already be sanitized.
func AdaptHTMLForViewport(body string, vp Viewport) string {
	if body == "" {
		return body
	}
	ua := ParseUAClass(vp.UAClass)
	bucket := ViewportBucket(vp.Width)
	// Parse the body fragment directly: html.Parse re-homes the content
	// under a synthesized <body>, so the mail's own top-level elements
	// become body children and are recognized as top-level below.
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return wrapViewport(body, ua, bucket, vp.ColorScheme, vp.TextScale)
	}
	root := findBody(doc)
	if root == nil {
		root = doc
	}
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		rewriteFixedWidths(c, true)
	}
	rewriteMaxWidthCaps(root)
	if bucket == "narrow" {
		rewriteWideInsets(root)
		dropNarrowMinWidths(root)
	}
	var buf strings.Builder
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&buf, c); err != nil {
			return wrapViewport(body, ua, bucket, vp.ColorScheme, vp.TextScale)
		}
	}
	return wrapViewport(buf.String(), ua, bucket, vp.ColorScheme, vp.TextScale)
}

func wrapViewport(inner, ua, bucket string, scheme ColorScheme, scale TextScale) string {
	class := fmt.Sprintf("urmail-vp urmail-vp-%s urmail-ua-%s", bucket, ua)
	if scheme == ColorSchemeDark {
		class += " urmail-scheme-dark"
	}
	switch scale {
	case TextScaleSmall:
		class += " urmail-scale-sm"
	case TextScaleLarge:
		class += " urmail-scale-lg"
	}
	style := viewportCSS()
	// The color-scheme declaration rides at the fragment head: the render
	// fragment is loaded as an iframe document, so the meta lands in that
	// document's head and the browser may render native widgets for the
	// pane in the requested scheme. With no explicit request the browser
	// preference applies; email content colors stay untouched either way.
	var meta string
	switch scheme {
	case ColorSchemeLight:
		meta = `light`
	case ColorSchemeDark:
		meta = `dark`
	default:
		meta = `light dark`
	}
	return `<meta name="color-scheme" content="` + meta + `">` + `<style type="text/css">` + style + `</style><div class="` + class + `">` + inner + `</div>`
}

// viewportCSS returns the wrapper stylesheet. The max-width cap targets
// layout elements only (table/td/th/img): a universal *{max-width} also
// lands on flex and inline-block children and was one of the blunt tools
// that crushed modern-mail layouts into a vertical column.
//
// The trailing prefers-color-scheme rule is the minimal dark support
// (Gmail-style restraint): it declares the scheme so native widgets
// (scrollbars, form controls) follow the user preference; it never
// inverts or recolors the email itself.
func viewportCSS() string {
	return strings.TrimSpace(`
.urmail-vp{max-width:100%;overflow-x:auto;box-sizing:border-box}
.urmail-vp *{box-sizing:border-box}
.urmail-vp table,.urmail-vp td,.urmail-vp th,.urmail-vp img{max-width:100%!important}
.urmail-vp img,.urmail-vp table{height:auto!important}
.urmail-vp-narrow{width:100%}
.urmail-vp-medium{width:100%}
.urmail-vp-wide{width:100%}
@media (max-width:640px){.urmail-vp{font-size:16px}}
@media (prefers-color-scheme:dark){.urmail-vp{color-scheme:light dark}}
.urmail-scheme-dark{color-scheme:dark}
.urmail-scale-sm{font-size:.875em}
.urmail-scale-lg{font-size:1.125em}
`)
}

// fluidWidthThreshold is the design width a row or column group must reach
// before it is rescaled to percentages. Rows that already fit inside the
// narrowest reading pane keep their markup: rescaling a 36px icon cell to
// 60% of the pane would stretch it far beyond what the designer intended.
const fluidWidthThreshold = 320

// rewriteWideInsets clamps oversized fixed-pixel horizontal margins and
// paddings on narrow panes. Desktop designs use wide gutters (Slack's
// marketing mail sets margin: 0 50px), which on a 375px pane cost a
// quarter of the width and leave a large blank band beside the text.
// Auto values (centering) and small insets stay untouched; only px values
// at or above narrowInsetThreshold become narrowInsetReplacement. Runs
// only for the narrow bucket, after the width rewrites.
func rewriteWideInsets(n *html.Node) {
	if n == nil {
		return
	}
	if n.Type == html.ElementNode {
		for i := range n.Attr {
			if !strings.EqualFold(n.Attr[i].Key, "style") {
				continue
			}
			n.Attr[i].Val = insetStyleRE.ReplaceAllStringFunc(n.Attr[i].Val, func(match string) string {
				m := insetStyleRE.FindStringSubmatch(match)
				if len(m) < 4 {
					return match
				}
				px, err := strconv.Atoi(m[2])
				if err != nil || px < narrowInsetThreshold {
					return match
				}
				return m[1] + ":" + strconv.Itoa(narrowInsetReplacement) + "px" + m[3]
			})
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		rewriteWideInsets(c)
	}
}

// rewriteMaxWidthCaps restates oversized fixed-pixel max-width caps as 100%
// across every element: a max-width is already a cap, so pointing it at the
// reading pane keeps the design intent without the overflow risk. Small
// caps below the fluid threshold are design details and stay untouched.
// Runs for every bucket, after the width rewrites (which never read
// max-width, so the order is observationally equivalent).
func rewriteMaxWidthCaps(n *html.Node) {
	if n == nil {
		return
	}
	if n.Type == html.ElementNode {
		for i := range n.Attr {
			if !strings.EqualFold(n.Attr[i].Key, "style") {
				continue
			}
			n.Attr[i].Val = maxWidthStyleRE.ReplaceAllStringFunc(n.Attr[i].Val, func(match string) string {
				m := maxWidthStyleRE.FindStringSubmatch(match)
				if len(m) < 3 {
					return match
				}
				px, err := strconv.Atoi(m[2])
				if err != nil || px < fluidWidthThreshold {
					return match
				}
				return m[1] + "100%"
			})
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		rewriteMaxWidthCaps(c)
	}
}

// dropNarrowMinWidths removes oversized fixed-pixel min-width declarations on
// narrow panes. A min-width at or above the fluid threshold cannot fit a
// 375px pane and forces horizontal overflow; the declaration is deleted
// outright rather than clamped, because any fixed floor above the pane is
// wrong. Small floors and wider buckets keep the design untouched. Runs only
// for the narrow bucket, after the width rewrites.
func dropNarrowMinWidths(n *html.Node) {
	if n == nil {
		return
	}
	if n.Type == html.ElementNode {
		for i := range n.Attr {
			if !strings.EqualFold(n.Attr[i].Key, "style") {
				continue
			}
			n.Attr[i].Val = minWidthStyleRE.ReplaceAllStringFunc(n.Attr[i].Val, func(match string) string {
				m := minWidthStyleRE.FindStringSubmatch(match)
				if len(m) < 2 {
					return match
				}
				px, err := strconv.Atoi(m[1])
				if err != nil || px < fluidWidthThreshold {
					return match
				}
				return "" // drop declaration and one adjoining separator
			})
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		dropNarrowMinWidths(c)
	}
}

// rewriteFixedWidths rewrites fixed widths so marketing mail adapts to the
// reading pane. Tables, images and TOP-LEVEL divs become fluid; the CELLS
// of a multi-column row keep their relative sizes by being rewritten to
// percentages of the row total. Forcing a cell to width:100% (the previous
// behavior) makes every column of a row claim the full pane, which is what
// collapsed multi-column mail into one crushed, vertical-text column. Rows
// that fit into the narrowest pane, rows with cells of unknown width and
// span-bridged rows keep their markup and are only capped with max-width.
// isTop marks the outermost containers (direct children of the mail body);
// nested divs without an explicit width keep their markup (see
// rewriteFluidWidths).
func rewriteFixedWidths(n *html.Node, isTop bool) {
	if n == nil {
		return
	}
	if n.Type == html.ElementNode {
		switch n.DataAtom {
		case atom.Tr:
			rewriteRowCellWidths(n)
		case atom.Colgroup:
			rewriteColumnGroupWidths(n)
		case atom.Table, atom.Div, atom.Img:
			// Tables are the layout skeleton and stay fluid at any depth;
			// divs only when they are top-level containers.
			rewriteFluidWidths(n, isTop || n.DataAtom == atom.Table)
		case atom.Td, atom.Th:
			ensureMaxWidth(&n.Attr)
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		rewriteFixedWidths(c, false)
	}
}

// rewriteFluidWidths turns a full-width element (table, div, img) fluid:
// large fixed widths stop clipping the reading pane by becoming 100%.
// When forceFluid is set the element also gains the fluid style outright;
// nested divs without it keep their markup when they declare no width of
// their own, so flex and inline-block wrappers stop being stretched into
// stacked full-width blocks.
func rewriteFluidWidths(n *html.Node, forceFluid bool) {
	// Style widths are rewritten first so the final fluid style below
	// recognizes them and never appends a duplicate style attribute.
	for i := range n.Attr {
		if strings.EqualFold(n.Attr[i].Key, "style") {
			n.Attr[i].Val = rewriteWidthStyle(n.Attr[i].Val)
		}
	}
	if n.DataAtom == atom.Img {
		for i := range n.Attr {
			if !strings.EqualFold(n.Attr[i].Key, "width") {
				continue
			}
			if m := widthAttrRE.FindStringSubmatch(n.Attr[i].Val); m != nil {
				if px, _ := strconv.Atoi(m[1]); px >= fluidWidthThreshold {
					// Keep the attribute for legacy clients but force fluid CSS.
					n.Attr[i].Val = "100%"
				}
			}
		}
		return
	}
	for i := range n.Attr {
		if !strings.EqualFold(n.Attr[i].Key, "width") {
			continue
		}
		if m := widthAttrRE.FindStringSubmatch(n.Attr[i].Val); m != nil {
			if px, _ := strconv.Atoi(m[1]); px >= fluidWidthThreshold {
				n.Attr[i].Val = "100%"
			}
		}
	}
	// Top-level containers and tables fill the pane so the layout scales
	// as a whole. Nested divs with no width of their own are left alone:
	// forcing width:100% on them stacked flex and inline-block rows into
	// a single vertical column.
	if forceFluid {
		ensureFluidStyle(&n.Attr)
	}
}

// rewriteRowCellWidths replaces the pixel widths of a row's cells with
// percentages of the row total so every column keeps its relative size at
// any pane width. Rows that cannot be measured safely are left untouched.
func rewriteRowCellWidths(tr *html.Node) {
	var cells []*html.Node
	for c := tr.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && (c.DataAtom == atom.Td || c.DataAtom == atom.Th) {
			cells = append(cells, c)
		}
	}
	if len(cells) == 0 {
		return
	}
	widths := make([]int, len(cells))
	total := 0
	for i, cell := range cells {
		for _, a := range cell.Attr {
			lower := strings.ToLower(a.Key)
			if lower == "colspan" || lower == "rowspan" {
				return // a spanning cell crosses columns; percentages would misplace it
			}
		}
		px, ok := fixedPixelWidth(cell)
		if !ok {
			return // an auto or percentage cell; scaling the row would distort it
		}
		widths[i] = px
		total += px
	}
	if total < fluidWidthThreshold {
		return // the row fits the narrowest pane; keep the designed sizes
	}
	for i, cell := range cells {
		pct := 100.0
		if len(cells) > 1 {
			pct = float64(widths[i]) / float64(total) * 100
		}
		setCellWidthPercent(cell, pct)
	}
}

// rewriteColumnGroupWidths applies the same rescaling to <col> widths, which
// otherwise override the cell widths of table-layout columns.
func rewriteColumnGroupWidths(group *html.Node) {
	var cols []*html.Node
	for c := group.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.DataAtom == atom.Col {
			cols = append(cols, c)
		}
	}
	if len(cols) == 0 {
		return
	}
	widths := make([]int, len(cols))
	total := 0
	for i, col := range cols {
		for _, a := range col.Attr {
			if strings.EqualFold(a.Key, "span") {
				return
			}
		}
		px, ok := fixedPixelWidth(col)
		if !ok {
			return
		}
		widths[i] = px
		total += px
	}
	if total < fluidWidthThreshold {
		return
	}
	for i, col := range cols {
		pct := 100.0
		if len(cols) > 1 {
			pct = float64(widths[i]) / float64(total) * 100
		}
		val := formatPercent(pct)
		for j := range col.Attr {
			switch strings.ToLower(col.Attr[j].Key) {
			case "width":
				col.Attr[j].Val = val
			case "style":
				col.Attr[j].Val = replaceWidthStyle(col.Attr[j].Val, val)
			}
		}
	}
}

// fixedPixelWidth reads a cell or column width from the inline style first,
// then from the width attribute. Percentage widths and unknown values are
// reported as not measurable.
func fixedPixelWidth(n *html.Node) (int, bool) {
	for _, a := range n.Attr {
		switch strings.ToLower(a.Key) {
		case "style":
			if m := widthStyleRE.FindStringSubmatch(a.Val); m != nil {
				if px, err := strconv.Atoi(m[2]); err == nil && px > 0 {
					return px, true
				}
			}
		case "width":
			if m := widthAttrRE.FindStringSubmatch(a.Val); m != nil {
				if px, err := strconv.Atoi(m[1]); err == nil && px > 0 {
					return px, true
				}
			}
		}
	}
	return 0, false
}

// setCellWidthPercent rewrites both the width attribute and an inline pixel
// width to a percentage of the row total.
func setCellWidthPercent(cell *html.Node, pct float64) {
	val := formatPercent(pct)
	for i := range cell.Attr {
		switch strings.ToLower(cell.Attr[i].Key) {
		case "width":
			cell.Attr[i].Val = val
		case "style":
			cell.Attr[i].Val = replaceWidthStyle(cell.Attr[i].Val, val)
		}
	}
}

// replaceWidthStyle swaps every pixel width declaration of a style attribute
// for the given width value, preserving the surrounding declarations.
func replaceWidthStyle(style, val string) string {
	return widthStyleRE.ReplaceAllStringFunc(style, func(match string) string {
		m := widthStyleRE.FindStringSubmatch(match)
		if len(m) < 3 {
			return match
		}
		return m[1] + "width:" + val + m[3]
	})
}

// formatPercent renders a share of the row total with at most two decimals,
// clamped so rounding can never hide a column completely.
func formatPercent(pct float64) string {
	if pct < 1 {
		pct = 1
	}
	if pct > 100 {
		pct = 100
	}
	return strconv.FormatFloat(math.Round(pct*100)/100, 'f', -1, 64) + "%"
}

// ensureMaxWidth caps a cell so it can never exceed the reading pane. It
// deliberately does NOT force width:100%: a full-width declaration on a
// table cell collapses multi-column mail.
func ensureMaxWidth(attrs *[]html.Attribute) {
	for i := range *attrs {
		if strings.EqualFold((*attrs)[i].Key, "style") {
			val := (*attrs)[i].Val
			if !strings.Contains(strings.ToLower(val), "max-width") {
				if val != "" && !strings.HasSuffix(strings.TrimSpace(val), ";") {
					val += ";"
				}
				(*attrs)[i].Val = val + "max-width:100%"
			}
			return
		}
	}
	*attrs = append(*attrs, html.Attribute{Key: "style", Val: "max-width:100%"})
}

func rewriteWidthStyle(style string) string {
	if style == "" {
		return style
	}
	return widthStyleRE.ReplaceAllStringFunc(style, func(match string) string {
		m := widthStyleRE.FindStringSubmatch(match)
		if len(m) < 3 {
			return match
		}
		px, _ := strconv.Atoi(m[2])
		if px < 320 {
			return match
		}
		prefix := m[1]
		suffix := m[3]
		if prefix == "" {
			return "max-width:100%;width:100%" + suffix
		}
		return prefix + "max-width:100%;width:100%" + suffix
	})
}

// hasExplicitWidthRE reports whether a style value already declares width
// in any form (px, %, auto, calc). Such an element only needs the cap:
// appending width:100% after an authored width in the same declaration list
// would override it, including fluid calc() values.
var hasExplicitWidthRE = regexp.MustCompile(`(?i)(?:^|;)\s*width\s*:`)

func ensureFluidStyle(attrs *[]html.Attribute) {
	for i := range *attrs {
		if strings.EqualFold((*attrs)[i].Key, "style") {
			val := (*attrs)[i].Val
			if !strings.Contains(strings.ToLower(val), "max-width") {
				if val != "" && !strings.HasSuffix(strings.TrimSpace(val), ";") {
					val += ";"
				}
				if hasExplicitWidthRE.MatchString(val) {
					(*attrs)[i].Val = val + "max-width:100%"
				} else {
					(*attrs)[i].Val = val + "max-width:100%;width:100%"
				}
			}
			return
		}
	}
	*attrs = append(*attrs, html.Attribute{Key: "style", Val: "max-width:100%;width:100%"})
}

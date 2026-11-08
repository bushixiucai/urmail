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
	widthAttrRE  = regexp.MustCompile(`(?i)^\s*(\d+)\s*(px)?\s*$`)
	widthStyleRE = regexp.MustCompile(`(?i)(^|;)\s*width\s*:\s*(\d+)\s*px\s*(;|$)`)
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
	doc, err := html.Parse(strings.NewReader("<div>" + body + "</div>"))
	if err != nil {
		return wrapViewport(body, ua, bucket)
	}
	root := findBody(doc)
	if root == nil {
		root = doc
	}
	rewriteFixedWidths(root)
	var buf strings.Builder
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&buf, c); err != nil {
			return wrapViewport(body, ua, bucket)
		}
	}
	return wrapViewport(buf.String(), ua, bucket)
}

func wrapViewport(inner, ua, bucket string) string {
	class := fmt.Sprintf("urmail-vp urmail-vp-%s urmail-ua-%s", bucket, ua)
	style := viewportCSS()
	return `<style type="text/css">` + style + `</style><div class="` + class + `">` + inner + `</div>`
}

func viewportCSS() string {
	return strings.TrimSpace(`
.urmail-vp{max-width:100%;overflow-x:auto;box-sizing:border-box}
.urmail-vp,.urmail-vp *{max-width:100%!important;box-sizing:border-box}
.urmail-vp img,.urmail-vp table{height:auto!important}
.urmail-vp-narrow{width:100%}
.urmail-vp-medium{width:100%}
.urmail-vp-wide{width:100%}
@media (max-width:640px){.urmail-vp{font-size:16px}}
`)
}

// fluidWidthThreshold is the design width a row or column group must reach
// before it is rescaled to percentages. Rows that already fit inside the
// narrowest reading pane keep their markup: rescaling a 36px icon cell to
// 60% of the pane would stretch it far beyond what the designer intended.
const fluidWidthThreshold = 320

// rewriteFixedWidths rewrites fixed widths so marketing mail adapts to the
// reading pane. Tables, divs and images become fluid; the CELLS of a
// multi-column row keep their relative sizes by being rewritten to
// percentages of the row total. Forcing a cell to width:100% (the previous
// behavior) makes every column of a row claim the full pane, which is what
// collapsed multi-column mail into one crushed, vertical-text column. Rows
// that fit into the narrowest pane, rows with cells of unknown width and
// span-bridged rows keep their markup and are only capped with max-width.
func rewriteFixedWidths(n *html.Node) {
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
			rewriteFluidWidths(n)
		case atom.Td, atom.Th:
			ensureMaxWidth(&n.Attr)
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		rewriteFixedWidths(c)
	}
}

// rewriteFluidWidths turns a full-width element (table, div, img) fluid:
// large fixed widths stop clipping the reading pane by becoming 100%.
func rewriteFluidWidths(n *html.Node) {
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
	// Tables and wrapper divs fill the pane so the layout scales as a whole.
	ensureFluidStyle(&n.Attr)
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

func ensureFluidStyle(attrs *[]html.Attribute) {
	for i := range *attrs {
		if strings.EqualFold((*attrs)[i].Key, "style") {
			val := (*attrs)[i].Val
			if !strings.Contains(strings.ToLower(val), "max-width") {
				if val != "" && !strings.HasSuffix(strings.TrimSpace(val), ";") {
					val += ";"
				}
				(*attrs)[i].Val = val + "max-width:100%;width:100%"
			}
			return
		}
	}
	*attrs = append(*attrs, html.Attribute{Key: "style", Val: "max-width:100%;width:100%"})
}

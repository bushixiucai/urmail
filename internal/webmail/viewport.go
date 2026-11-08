package webmail

import (
	"fmt"
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

func rewriteFixedWidths(n *html.Node) {
	if n == nil {
		return
	}
	if n.Type == html.ElementNode {
		for i := range n.Attr {
			key := strings.ToLower(n.Attr[i].Key)
			switch key {
			case "width":
				if m := widthAttrRE.FindStringSubmatch(n.Attr[i].Val); m != nil {
					if px, _ := strconv.Atoi(m[1]); px >= 320 {
						// Keep the attribute for legacy clients but force fluid CSS.
						n.Attr[i].Val = "100%"
						ensureFluidStyle(&n.Attr)
					}
				}
			case "style":
				n.Attr[i].Val = rewriteWidthStyle(n.Attr[i].Val)
			}
		}
		// Tables with a large fixed width attribute are the usual offender.
		if n.DataAtom == atom.Table || n.DataAtom == atom.Td || n.DataAtom == atom.Th || n.DataAtom == atom.Div {
			ensureFluidStyle(&n.Attr)
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		rewriteFixedWidths(c)
	}
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

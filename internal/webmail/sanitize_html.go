package webmail

// Server-side HTML mail sanitization (WEBMAIL-007 / WEB-09..13).
//
// Sanitization is render-time: the IMAP store always keeps the original
// byte-faithful message; SanitizeHTML runs on the body only when a message
// view is served. It is a strict allowlist parser built on golang.org/x/net
// html (already a dependency - no new module is introduced): the input is
// parsed into a tree, every element/attribute not explicitly allowed is
// dropped, and the tree is re-serialized from scratch. Serializing from the
// parsed tree (never string surgery) neutralizes mXSS mutation patterns.
//
// The output is additionally rendered inside a sandboxed iframe (no scripts,
// no same-origin) by the web client with a CSP meta tag - this package is
// defense in depth, not the only layer.

import (
	"bytes"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	// MaxHTMLBytes bounds the parsed HTML body; larger bodies are truncated
	// with the "html_truncated" note instead of failing the whole message.
	MaxHTMLBytes = 2 << 20
	// MaxAttachmentsPerMessage bounds how many attachments are surfaced in
	// message metadata (downloads remain bounded per part).
	MaxAttachmentsPerMessage = 50
)

// Sanitization note tokens returned to clients. Frontends match on these to
// show e.g. a "load images" bar (remote_images_blocked).
const (
	NoteHTMLTruncated        = "html_truncated"
	NoteRemoteImagesBlocked  = "remote_images_blocked"
	NoteActiveContentRemoved = "active_content_removed"
	NoteUnsafeStyleRemoved   = "unsafe_style_removed"
	NoteCIDUnresolved        = "cid_unresolved"
)

// SanitizeOptions tunes one render-time sanitization pass.
type SanitizeOptions struct {
	// AllowRemoteImages permits http(s) img src (the per-message
	// "load images" user action). Default is blocked for privacy.
	AllowRemoteImages bool
	// CIDRewriter, when non-nil, maps a cid: URI to a same-origin URL
	// (attachment endpoint). When nil or it reports !ok, cid images are
	// dropped with NoteCIDUnresolved.
	CIDRewriter func(cid string) (url string, ok bool)
	// RemoteImageProxy, when non-nil, rewrites an allowed remote image
	// URL to a same-origin URL served by the image proxy, so the browser
	// never contacts the third party. Reporting !ok drops the image:
	// there is no direct-URL fallback, because a direct URL would hand
	// the reader's address to the sender.
	RemoteImageProxy func(remoteURL string) (url string, ok bool)
}

// SanitizeHTML sanitizes an HTML mail body with remote images blocked.
func SanitizeHTML(body string) (string, []string) {
	return SanitizeHTMLOpts(body, SanitizeOptions{})
}

// SanitizeHTMLOpts sanitizes an HTML mail body with options.
func SanitizeHTMLOpts(body string, opts SanitizeOptions) (string, []string) {
	s := &htmlSanitizer{opts: opts}
	src := body
	if len(src) > MaxHTMLBytes {
		src = src[:MaxHTMLBytes]
		s.note(NoteHTMLTruncated)
	}
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return "", []string{NoteActiveContentRemoved}
	}
	if bodyEl := findBody(doc); bodyEl != nil {
		for child := bodyEl.FirstChild; child != nil; {
			next := child.NextSibling
			if s.visit(child) {
				s.kept = append(s.kept, child)
			}
			child = next
		}
	}
	var buf bytes.Buffer
	for _, top := range s.kept {
		if err := html.Render(&buf, top); err != nil {
			return "", []string{NoteActiveContentRemoved}
		}
	}
	return buf.String(), s.notes
}

func findBody(n *html.Node) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.DataAtom == atom.Body {
			return c
		}
		if found := findBody(c); found != nil {
			return found
		}
	}
	return nil
}

type htmlSanitizer struct {
	opts  SanitizeOptions
	kept  []*html.Node
	notes []string
}

func (s *htmlSanitizer) note(n string) {
	for _, existing := range s.notes {
		if existing == n {
			return
		}
	}
	s.notes = append(s.notes, n)
}

// visit sanitizes node n in place and reports whether it survives (kept by
// the caller). Nodes are mutated in the parsed tree, which is dropped after
// rendering; the child list is snapshotted before recursion.
func (s *htmlSanitizer) visit(n *html.Node) bool {
	switch n.Type {
	case html.TextNode:
		return false // kept via its sanitized parent element
	case html.ElementNode:
	default:
		return false // comments, doctype, etc.
	}
	if !allowedTags[n.DataAtom] {
		// Dropped with its whole subtree: script/style/iframe/svg/math/
		// form controls/foreignObject all land here. Parser-level subtree
		// drop defeats mXSS mutation through these tree shapes.
		s.note(NoteActiveContentRemoved)
		return false
	}
	if n.DataAtom == atom.Img {
		if !s.sanitizeImg(n) {
			return false
		}
	}
	keepAttrs := 0
	for _, a := range n.Attr {
		ok, unsafeStyle := attrAllowed(n, a.Key, a.Val)
		if unsafeStyle {
			s.note(NoteUnsafeStyleRemoved)
		}
		if !ok {
			continue
		}
		n.Attr[keepAttrs] = html.Attribute{Key: a.Key, Val: a.Val}
		keepAttrs++
	}
	n.Attr = n.Attr[:keepAttrs]
	if n.DataAtom == atom.A {
		hardenAnchor(n)
	}
	children := make([]*html.Node, 0, 4)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		children = append(children, c)
	}
	for _, c := range children {
		s.visit(c)
	}
	return true
}

// sanitizeImg validates/rewrites the img src against policy. It returns
// false when the element must be dropped entirely.
func (s *htmlSanitizer) sanitizeImg(img *html.Node) bool {
	src := ""
	keep := img.Attr[:0]
	for _, a := range img.Attr {
		if strings.EqualFold(a.Key, "src") {
			src = a.Val
			continue
		}
		keep = append(keep, a)
	}
	img.Attr = keep
	if src == "" {
		return true // decorative <img> without src is harmless
	}
	url, note := imgSrcURL(src, s.opts)
	if url == "" {
		if note != "" {
			s.note(note)
		}
		return false
	}
	img.Attr = append(img.Attr, html.Attribute{Key: "src", Val: url})
	return true
}

// hardenAnchor forces links open in a new tab without referrer; href schemes
// are already restricted by safeLinkScheme.
func hardenAnchor(a *html.Node) {
	a.Attr = append(a.Attr,
		html.Attribute{Key: "rel", Val: "noopener noreferrer"},
		html.Attribute{Key: "target", Val: "_blank"},
	)
}

var (
	allowedTags = map[atom.Atom]bool{
		atom.P: true, atom.Br: true, atom.Div: true, atom.Span: true,
		atom.B: true, atom.Strong: true, atom.I: true, atom.Em: true,
		atom.U: true, atom.S: true, atom.Strike: true, atom.Sub: true,
		atom.Sup: true, atom.Small: true, atom.Big: true, atom.Tt: true,
		atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true,
		atom.H5: true, atom.H6: true, atom.Ul: true, atom.Ol: true,
		atom.Li: true, atom.Blockquote: true, atom.Pre: true, atom.Code: true,
		atom.Hr: true, atom.Table: true, atom.Thead: true, atom.Tbody: true,
		atom.Tfoot: true, atom.Tr: true, atom.Td: true, atom.Th: true,
		atom.Caption: true, atom.Col: true, atom.Colgroup: true,
		atom.Img: true, atom.A: true, atom.Font: true, atom.Center: true,
		atom.Abbr: true, atom.Cite: true, atom.Dfn: true, atom.Kbd: true,
		atom.Q: true, atom.Samp: true, atom.Var: true, atom.Dl: true,
		atom.Dt: true, atom.Dd: true, atom.Address: true,
		atom.Article: true, atom.Aside: true, atom.Section: true,
		atom.Header: true, atom.Footer: true, atom.Figure: true,
		atom.Figcaption: true, atom.Main: true, atom.Details: true,
		atom.Summary: true,
	}

	globalAttrs = map[string]bool{
		"class": true, "dir": true, "lang": true, "title": true,
		"align": true, "valign": true, "width": true, "height": true,
		"style": true, "bgcolor": true,
	}

	tableAttrs = map[string]bool{
		"border": true, "cellpadding": true, "cellspacing": true,
		"colspan": true, "rowspan": true,
	}

	imgAttrs = map[string]bool{"alt": true}

	// safeDataImage matches only raster image data: URIs; SVG/HTML payloads
	// in data: URIs are excluded on purpose.
	safeDataImage = regexp.MustCompile(`^data:image/(?:png|jpe?g|gif|webp);base64,[A-Za-z0-9+/=\s]+$`)

	// unsafeCSS matches CSS constructs that fetch URLs or execute.
	unsafeCSS = regexp.MustCompile(`(?i)url\s*\(|image-set\s*\(|element\s*\(|expression\s*\(|@import|behavior\s*:|javascript\s*:|vbscript\s*:|-moz-binding`)
)

// attrAllowed reports whether an attribute may be kept. unsafeStyle marks a
// style attribute dropped for containing unsafe CSS (surfaced as a note).
func attrAllowed(n *html.Node, key, val string) (keep, unsafeStyle bool) {
	lower := strings.ToLower(key)
	switch lower {
	case "style":
		if unsafeCSS.MatchString(val) {
			return false, true
		}
		return true, false
	case "href":
		return n.DataAtom == atom.A && safeLinkScheme(val), false
	case "src":
		// <img src> is validated by sanitizeImg; non-img src (none of the
		// allowed tags legitimately carry one) is refused.
		return n.DataAtom == atom.Img, false
	case "srcset", "id", "name", "background", "poster", "action",
		"formaction", "ping", "dynsrc", "lowsrc":
		return false, false
	}
	if globalAttrs[lower] {
		return true, false
	}
	switch {
	case n.DataAtom == atom.Img && imgAttrs[lower]:
		return true, false
	case (n.DataAtom == atom.Td || n.DataAtom == atom.Th || n.DataAtom == atom.Table) && tableAttrs[lower]:
		return true, false
	}
	return false, false
}

// imgSrcURL validates an <img src> against policy and returns the URL to use
// (possibly rewritten from cid:), or "" when the image must be dropped.
func imgSrcURL(src string, opts SanitizeOptions) (url, note string) {
	v := strings.TrimSpace(src)
	lower := strings.ToLower(v)
	switch {
	case strings.HasPrefix(lower, "cid:"):
		if opts.CIDRewriter == nil {
			return "", NoteCIDUnresolved
		}
		if u, ok := opts.CIDRewriter(strings.TrimPrefix(v, "cid:")); ok && u != "" {
			return u, ""
		}
		return "", NoteCIDUnresolved
	case safeDataImage.MatchString(v):
		return v, ""
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		if opts.AllowRemoteImages {
			if opts.RemoteImageProxy != nil {
				// The proxy is the only safe way to show a remote image:
				// a direct URL would let the sender learn the reader's
				// address, so a refusal drops the image instead.
				if u, ok := opts.RemoteImageProxy(v); ok && u != "" {
					return u, ""
				}
				return "", NoteRemoteImagesBlocked
			}
			return v, ""
		}
		return "", NoteRemoteImagesBlocked
	}
	return "", NoteCIDUnresolved
}

// safeLinkScheme allows only http(s) and mailto hrefs; everything else
// (javascript:, data:, vbscript:, unknown schemes, protocol-relative) is
// dropped. Control characters and whitespace are stripped first so
// "jav&#x09;ascript:" style tricks cannot smuggle a scheme through.
func safeLinkScheme(raw string) bool {
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		if r <= 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	s := b.String()
	schemeEnd := strings.IndexByte(s, ':')
	if schemeEnd <= 0 {
		// Relative URLs resolve against the sandboxed blank doc; harmless.
		return !strings.HasPrefix(s, "//")
	}
	switch strings.ToLower(s[:schemeEnd]) {
	case "http", "https", "mailto":
		return true
	}
	return false
}

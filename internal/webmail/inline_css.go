package webmail

import (
	"regexp"
	"strings"

	premailer "github.com/vanng822/go-premailer/premailer"
)

// Style-selector inlining (v0.6.12 PR-6): real-world marketing mail keeps
// its layout in <style> rules that the sanitizer drops wholesale, so the
// element styling never reaches the rendered fragment. InlineCSS runs
// before sanitization (inline → sanitize → adapt, in that order):
//
//   - @font-face blocks are stripped — webfonts are fetch vectors and the
//     sandbox renders with system fonts anyway;
//   - @media blocks are evaluated server-side against the reading bucket:
//     the mobile bucket unwraps max-width blocks (their responsive rules
//     are the fix for phone rendering), every other media block is dropped;
//   - the remaining rules are inlined into style attributes by
//     go-premailer; leftover pseudo-class rules come back in a style
//     element and are dropped by the sanitizer, which runs next.
//
// The result keys into the render cache by bucket, so one evaluation per
// bucket is the whole contract.

// styleTagRE captures the body of one <style> element.
var styleTagRE = regexp.MustCompile(`(?is)(<style[^>]*>)(.*?)(</style>)`)

// InlineCSS inlines the document's <style> rules for the reading bucket
// (desktop / mobile / tablet, empty = desktop). Documents without style
// elements are returned untouched; on any preprocessing or inlining
// error the input comes back unchanged — the sanitizer's whole-attribute
// behavior of dropping styles remains the safe fallback.
func InlineCSS(doc string, bucket string) string {
	if !strings.Contains(doc, "<style") {
		return doc
	}
	doc = styleTagRE.ReplaceAllStringFunc(doc, func(m string) string {
		parts := styleTagRE.FindStringSubmatch(m)
		css, ok := adaptStylesheet(parts[2], bucket)
		if !ok {
			// Nothing inlinable remains; drop the element so the
			// sanitizer does not fight leftovers.
			return ""
		}
		return parts[1] + css + parts[3]
	})
	prem, err := premailer.NewPremailerFromBytes([]byte(doc), &premailer.Options{
		RemoveClasses:     false,
		CssToAttributes:   false,
		KeepBangImportant: false,
	})
	if err != nil {
		return doc
	}
	out, err := prem.Transform()
	if err != nil {
		return doc
	}
	return out
}

// adaptStylesheet rewrites one stylesheet for the bucket and reports
// whether any inlinable rules remain.
func adaptStylesheet(css, bucket string) (string, bool) {
	css = stripAtBlocks(css, "@font-face")
	css = rewriteMediaBlocks(css, bucket)
	return css, strings.TrimSpace(css) != ""
}

// rewriteMediaBlocks evaluates each @media block against the bucket: the
// mobile bucket unwraps max-width blocks so their responsive rules apply
// unconditionally, everything else is dropped.
func rewriteMediaBlocks(css, bucket string) string {
	var b strings.Builder
	rest := css
	for {
		at := indexAtKeyword(rest, "@media")
		if at < 0 {
			b.WriteString(rest)
			return b.String()
		}
		head := rest[:at]
		cond, body, after, ok := cutAtBlock(rest[at:])
		if !ok {
			// Unbalanced stylesheet: keep the remainder verbatim.
			b.WriteString(head)
			b.WriteString(rest[at:])
			return b.String()
		}
		b.WriteString(head)
		if bucket == UAClassMobile && strings.Contains(cond, "max-width") {
			// Unwrap: the inner rules ride the normal cascade and win
			// over the base rules they refine.
			b.WriteString(stripAtBlocks(body, "@font-face"))
			b.WriteString("\n")
		}
		rest = after
	}
}

// stripAtBlocks removes @name { ... } blocks (balanced braces) from a
// stylesheet.
func stripAtBlocks(css, keyword string) string {
	var b strings.Builder
	rest := css
	for {
		at := indexAtKeyword(rest, keyword)
		if at < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:at])
		_, body, after, ok := cutAtBlock(rest[at:])
		if !ok {
			b.WriteString(rest[at:])
			return b.String()
		}
		_ = body
		rest = after
	}
}

// indexAtKeyword finds the next occurrence of the at-keyword outside any
// word character context (so "@media" does not match "@mediax" and an
// earlier rule's braces do not matter — at-keywords cannot nest).
func indexAtKeyword(css, keyword string) int {
	at := 0
	for {
		i := strings.Index(css[at:], keyword)
		if i < 0 {
			return -1
		}
		pos := at + i
		before := byte(' ')
		if pos > 0 {
			before = css[pos-1]
		}
		after := byte(' ')
		if pos+len(keyword) < len(css) {
			after = css[pos+len(keyword)]
		}
		if !isCSSWordByte(before) && !isCSSWordByte(after) {
			return pos
		}
		at = pos + 1
	}
}

func isCSSWordByte(b byte) bool {
	return b == '-' || b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// cutAtBlock splits "kw-condition { body } rest" into its parts. Braces
// balance across comments and strings badly enough in mail CSS that a
// plain counter is honest: an unbalanced stylesheet leaves the block
// untouched (ok=false) and the caller keeps it verbatim.
func cutAtBlock(s string) (cond, body, rest string, ok bool) {
	open := strings.IndexByte(s, '{')
	if open < 0 {
		return "", "", "", false
	}
	cond = s[:open]
	depth := 1
	i := open + 1
	for ; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return cond, s[open+1 : i], s[i+1:], true
			}
		}
	}
	return "", "", "", false
}

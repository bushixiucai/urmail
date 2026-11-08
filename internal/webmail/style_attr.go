package webmail

import (
	"regexp"
	"strings"

	css "github.com/gorilla/css/scanner"
)

// Declaration-level handling of inline style attributes (v0.6.12 PR-5).
// A style attribute is no longer discarded wholesale when any unsafe CSS
// construct matches: declarations are processed one by one. Only
// background/background-image url() references are eligible for the
// remote-image proxy; every other declaration carrying a URL fetch or
// scripting construct is dropped on its own, and the remaining
// declarations are preserved.

// backgroundProps are the declarations whose url() references follow the
// <img src> policy (proxy when allowed, dropped otherwise). Every other
// declaration keeps the whole declaration only when it carries no unsafe
// construct at all.
var backgroundProps = map[string]bool{"background": true, "background-image": true}

// cssURLRef matches one url(...) reference inside a declaration value,
// quoted or bare. The scanner yields url() as a single URI token, but
// matching on the rebuilt declaration text keeps rewriting independent
// of token boundaries.
var cssURLRef = regexp.MustCompile(`(?i)url\(\s*(?:"([^"]*)"|'([^']*)'|([^)'"]*))\s*\)`)

// sanitizeStyleAttr filters one style attribute declaration by
// declaration. dropped=true removes the attribute outright; otherwise
// cleaned carries the kept declarations and equals the input untouched
// when nothing had to change. notes report what was dropped.
func sanitizeStyleAttr(val string, opts SanitizeOptions) (string, bool, []string) {
	if strings.TrimSpace(val) == "" {
		// Empty style attributes stay (they are carried by real-world
		// markup and downstream passes may fill them); nothing to filter.
		return val, false, nil
	}
	decls, ok := splitStyleDeclarations(val)
	if !ok {
		// Untokenizable CSS (unterminated quote or comment) keeps the
		// old conservative contract: drop the attribute rather than
		// guess where declarations end.
		return "", true, []string{NoteUnsafeStyleRemoved}
	}
	changed := false
	dropped := false
	blockedRemote := false
	kept := make([]string, 0, len(decls))
	for _, decl := range decls {
		if !unsafeCSS.MatchString(decl) {
			kept = append(kept, strings.TrimSpace(decl))
			continue
		}
		prop, body := splitStyleDecl(decl)
		if !backgroundProps[prop] || unsafeCSSNonURL.MatchString(decl) {
			dropped = true
			continue
		}
		rewritten, keep, blocked := proxyBackgroundURLs(body, opts)
		if !keep {
			dropped = true
			blockedRemote = blockedRemote || blocked
			continue
		}
		kept = append(kept, prop+": "+rewritten)
		changed = true
	}
	var outNotes []string
	if dropped {
		outNotes = append(outNotes, NoteUnsafeStyleRemoved)
	}
	if blockedRemote {
		outNotes = append(outNotes, NoteRemoteImagesBlocked)
	}
	if !changed && !dropped {
		return val, false, outNotes
	}
	if len(kept) == 0 {
		return "", true, outNotes
	}
	return strings.Join(kept, "; "), false, outNotes
}

// splitStyleDeclarations cuts a style attribute into declarations at
// semicolons that sit outside quotes and parentheses. The returned
// fragments are the raw declaration texts. It reports false when the CSS
// cannot be tokenized cleanly (scanner error or unbalanced parens), in
// which case callers must not guess.
func splitStyleDeclarations(val string) ([]string, bool) {
	s := css.New(val)
	var decls []string
	var cur strings.Builder
	depth := 0
	for {
		t := s.Next()
		if t == nil || t.Type == css.TokenEOF {
			break
		}
		if t.Type == css.TokenError {
			return nil, false
		}
		switch t.Type {
		case css.TokenChar:
			switch t.Value {
			case "(":
				depth++
			case ")":
				depth--
				if depth < 0 {
					return nil, false
				}
			case ";":
				if depth == 0 {
					decls = append(decls, cur.String())
					cur.Reset()
					continue
				}
			}
		case css.TokenFunction:
			// "name(" — the paren is part of the token text.
			depth++
		}
		cur.WriteString(t.Value)
	}
	if depth != 0 {
		return nil, false
	}
	if last := strings.TrimSpace(cur.String()); last != "" {
		decls = append(decls, last)
	}
	return decls, true
}

// splitStyleDecl separates a declaration into its property name and the
// value body. Property names never contain a colon, so the first colon
// is the separator; declarations without one are unparsable.
func splitStyleDecl(decl string) (prop, body string) {
	i := strings.IndexByte(decl, ':')
	if i < 0 {
		return "", decl
	}
	return strings.ToLower(strings.TrimSpace(decl[:i])), strings.TrimSpace(decl[i+1:])
}

// proxyBackgroundURLs rewrites every url() reference in a background
// declaration value through the image policy. It reports whether the
// rewritten value may be kept — every reference must resolve or the
// whole declaration is refused — and whether any refusal came from the
// remote-image policy specifically.
func proxyBackgroundURLs(body string, opts SanitizeOptions) (string, bool, bool) {
	failed := false
	blockedRemote := false
	out := cssURLRef.ReplaceAllStringFunc(body, func(match string) string {
		m := cssURLRef.FindStringSubmatch(match)
		ref := strings.TrimSpace(m[1] + m[2] + m[3])
		url, ok, blocked := styleImageURL(ref, opts)
		if !ok {
			failed = true
			blockedRemote = blockedRemote || blocked
			return match
		}
		if url == ref {
			return match
		}
		return `url("` + url + `")`
	})
	if failed {
		return out, false, blockedRemote
	}
	return out, true, false
}

// styleImageURL applies the <img src> policy to a CSS url() reference
// (see imgSrcURL): cid: has no attachment meaning inside CSS, safe
// raster data: URIs stay, http(s) follows the remote-image policy
// (proxy when allowed, refused otherwise) and everything else is
// refused. ok=false with blockedRemote=false marks a construct the
// unsafe-style note covers; ok=false with blockedRemote=true is the
// remote-images-blocked note.
func styleImageURL(ref string, opts SanitizeOptions) (url string, ok, blockedRemote bool) {
	lower := strings.ToLower(ref)
	switch {
	case strings.HasPrefix(lower, "cid:"):
		return "", false, false
	case strings.HasPrefix(lower, "data:"):
		if safeDataImage.MatchString(ref) {
			return ref, true, false
		}
		return "", false, false
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		if opts.AllowRemoteImages {
			if opts.RemoteImageProxy != nil {
				if u, ok := opts.RemoteImageProxy(ref); ok && u != "" {
					return u, true, false
				}
				return "", false, true
			}
			return ref, true, false
		}
		return "", false, true
	}
	return "", false, false
}

package webmail

import "strings"

// CSPMetaContent is the Content-Security-Policy for a sanitized mail
// document rendered inside a sandboxed iframe. Remote images are served
// through the same-origin proxy (or dropped), so img-src never needs a
// third-party host. data: covers inline raster images left by the
// sanitizer; 'self' covers cid rewrites and the image proxy.
func CSPMetaContent() string {
	return strings.Join([]string{
		"default-src 'none'",
		"img-src 'self' data:",
		"style-src 'unsafe-inline'",
		"font-src 'none'",
		"connect-src 'none'",
		"base-uri 'none'",
		"form-action 'none'",
		"frame-src 'none'",
		"media-src 'none'",
		"object-src 'none'",
		"script-src 'none'",
	}, "; ")
}

// WrapHTMLDocument turns a sanitized body fragment into a complete HTML
// document with a CSP meta tag in <head>. Clients that render via iframe
// srcDoc get the policy without depending on their own injection. The
// frontend sanitizer remains as defense in depth and may re-wrap.
func WrapHTMLDocument(body string) string {
	if body == "" {
		return ""
	}
	// Avoid double-wrapping when a previous pass already produced a document.
	lower := strings.ToLower(strings.TrimSpace(body))
	if strings.HasPrefix(lower, "<!doctype") || strings.HasPrefix(lower, "<html") {
		if strings.Contains(lower, "content-security-policy") {
			return body
		}
		// Insert CSP meta after <head> when present.
		if idx := strings.Index(lower, "<head>"); idx >= 0 {
			insertAt := idx + len("<head>")
			return body[:insertAt] + cspMetaTag() + body[insertAt:]
		}
		return body
	}
	return "<!doctype html><html><head><meta charset=\"utf-8\">" +
		cspMetaTag() +
		"</head><body>" + body + "</body></html>"
}

func cspMetaTag() string {
	return `<meta http-equiv="Content-Security-Policy" content="` + CSPMetaContent() + `">`
}

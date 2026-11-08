package webmail

import (
	"strings"
	"testing"
)

func TestWrapHTMLDocumentInjectsCSP(t *testing.T) {
	out := WrapHTMLDocument(`<p>hi</p>`)
	if !strings.HasPrefix(out, "<!doctype html>") {
		t.Fatalf("not a document: %s", out)
	}
	if !strings.Contains(out, "Content-Security-Policy") {
		t.Fatalf("csp missing: %s", out)
	}
	if !strings.Contains(out, "img-src 'self' data:") {
		t.Fatalf("img-src policy missing: %s", out)
	}
	if !strings.Contains(out, "script-src 'none'") {
		t.Fatalf("script-src missing: %s", out)
	}
	if !strings.Contains(out, "<p>hi</p>") {
		t.Fatalf("body lost: %s", out)
	}
}

func TestWrapHTMLDocumentIdempotent(t *testing.T) {
	first := WrapHTMLDocument(`<p>x</p>`)
	second := WrapHTMLDocument(first)
	if strings.Count(second, "Content-Security-Policy") != 1 {
		t.Fatalf("double csp: %s", second)
	}
}

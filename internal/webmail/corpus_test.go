package webmail

// Corpus regression tests (v0.6.12 render batch, work item 1).
//
// Every *.eml in testdata/corpus runs through the full rendering pipeline:
// MIME HTML-part extraction -> sanitize -> viewport adaptation -> CSP
// wrap. Two layers protect against regressions:
//
//  1. Structural invariants asserted HARD on every corpus file (no
//     scripts/iframes/forms, no event handlers, no blocked-image shells,
//     exactly one CSP meta, no multi-cell full-width rows).
//  2. The full wrapped output is compared against a golden file. After a
//     REVIEWED pipeline change, regenerate the goldens with
//     URMAIL_UPDATE_GOLDEN=1 go test ./internal/webmail/ -run Corpus
//     and inspect the diff.
//
// Real production samples are added through scripts/corpus-redact.py,
// which hashes sender/link domains while keeping the body text and MIME
// structure intact.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// corpusHTMLPart extracts and decodes the text/html part of a raw corpus
// message. Corpus fixtures always carry an HTML part; text-only mails are
// out of scope for the render pipeline.
func corpusHTMLPart(t *testing.T, raw []byte) string {
	t.Helper()
	header, body, ok := splitMessageHead(raw)
	if !ok {
		t.Fatalf("no header/body split")
	}
	fields := parseHeaderFields(header)
	mediaType, params := parseContentType(fields["content-type"])
	part, cte, charset, ok := corpusFindHTMLPart(body, mediaType, params, fields, 0)
	if !ok {
		t.Fatalf("no text/html part")
	}
	return string(decodeCharset(decodeTransferEncoding(part, cte), charset))
}

func corpusFindHTMLPart(body []byte, mediaType string, params map[string]string, fields map[string]string, depth int) ([]byte, string, string, bool) {
	if depth > 3 {
		return nil, "", "", false
	}
	if mediaType == "text/html" {
		return body, strings.ToLower(strings.TrimSpace(fields["content-transfer-encoding"])), params["charset"], true
	}
	if !strings.HasPrefix(mediaType, "multipart/") {
		return nil, "", "", false
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil, "", "", false
	}
	for _, part := range splitMultipartParts(body, boundary) {
		partFields := parseHeaderFields(part.header)
		partType, partParams := parseContentType(partFields["content-type"])
		if out, cte, charset, ok := corpusFindHTMLPart(part.body, partType, partParams, partFields, depth+1); ok {
			return out, cte, charset, true
		}
	}
	return nil, "", "", false
}

var corpusEventAttrRE = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)

// assertCorpusInvariants holds the pipeline's hard safety and layout
// contracts. sanitized is the post-sanitize body (mail-authored styles
// must be gone); out is the final wrapped output (exactly one style block,
// the pipeline's own viewport CSS).
func assertCorpusInvariants(t *testing.T, sanitized, out string) {
	t.Helper()
	if strings.Contains(strings.ToLower(sanitized), "<style") {
		t.Fatalf("sanitized body still carries a mail-authored <style> block")
	}
	lower := strings.ToLower(out)
	for _, banned := range []string{
		"<script", "<iframe", "<form", "<object", "<embed", "<video",
		"javascript:", "vbscript:",
	} {
		if strings.Contains(lower, banned) {
			t.Fatalf("output contains %q", banned)
		}
	}
	if styles := strings.Count(lower, "<style"); styles != 1 {
		t.Fatalf("style block count = %d, want exactly 1 (the viewport CSS)", styles)
	}
	if !strings.Contains(lower, "urmail-vp") {
		t.Fatalf("viewport wrapper missing")
	}
	if corpusEventAttrRE.MatchString(out) {
		t.Fatalf("output contains an inline event handler: %s", corpusEventAttrRE.FindString(out))
	}
	if imgs := strings.Count(lower, "<img"); imgs != 0 {
		// Corpus fixtures use remote images only; the blocked default must
		// leave no shells behind.
		t.Fatalf("blocked remote images left %d img shells", imgs)
	}
	if csp := strings.Count(lower, "http-equiv=\"content-security-policy\""); csp != 1 {
		t.Fatalf("CSP meta count = %d, want exactly 1", csp)
	}
}

// assertCorpusLayoutInvariants guards the structural M2 contract: no row
// carries two or more full-width direct cells. It runs on the desktop
// chain only, where every media block is dropped and no sender-authored
// responsive stacking applies — so a full-width cell here can only come
// from our own adaptation regressions (the PR-B blunt-instrument class
// of bug). On the mobile chain senders legitimately stack columns for
// phones (Slack's logo-social-row does exactly that).
func assertCorpusLayoutInvariants(t *testing.T, out string) {
	t.Helper()
	for _, row := range corpusRows(t, out) {
		if row.cells >= 2 && row.fullWidth >= 2 {
			t.Fatalf("collapsed row (%d cells, %d full-width) survived", row.cells, row.fullWidth)
		}
	}
}

type corpusRowStat struct {
	cells     int
	fullWidth int
}

var corpusFullWidthStyleRE = regexp.MustCompile(`(?i)(?:^|[^-])width\s*:\s*100%`)

func corpusRows(t *testing.T, doc string) []corpusRowStat {
	t.Helper()
	parsed, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	var rows []corpusRowStat
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Tr {
			stat := corpusRowStat{}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode && (c.DataAtom == atom.Td || c.DataAtom == atom.Th) {
					stat.cells++
					if corpusCellFullWidth(c) {
						stat.fullWidth++
					}
				}
			}
			rows = append(rows, stat)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(parsed)
	return rows
}

func corpusCellFullWidth(cell *html.Node) bool {
	for _, a := range cell.Attr {
		switch strings.ToLower(a.Key) {
		case "width":
			if strings.TrimSpace(a.Val) == "100%" {
				return true
			}
		case "style":
			if corpusFullWidthStyleRE.MatchString(a.Val) {
				return true
			}
		}
	}
	return false
}

func TestCorpusRenderGolden(t *testing.T) {
	paths, err := filepath.Glob("testdata/corpus/*.eml")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("corpus is empty")
	}
	update := os.Getenv("URMAIL_UPDATE_GOLDEN") == "1"
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			doc := corpusHTMLPart(t, raw)
			// Mirror the production pipeline order (inline → sanitize →
			// adapt) so the goldens pin the whole chain, media handling
			// included. The golden is the mobile render (phones are the
			// broken-layout surface); the invariants run on both chains —
			// the structural M2 check on the desktop chain where media is
			// dropped, so it can only catch our own regressions.
			inlined := InlineCSS(doc, UAClassMobile)
			safe, _ := SanitizeHTMLOpts(inlined, SanitizeOptions{})
			out := AdaptHTMLForViewport(safe, Viewport{UAClass: "mobile", Width: 375})
			wrapped := WrapHTMLDocument(out)
			assertCorpusInvariants(t, safe, wrapped)

			desktopInlined := InlineCSS(doc, UAClassDesktop)
			desktopSafe, _ := SanitizeHTMLOpts(desktopInlined, SanitizeOptions{})
			desktopWrapped := WrapHTMLDocument(AdaptHTMLForViewport(desktopSafe, Viewport{UAClass: UAClassDesktop, Width: 1024}))
			assertCorpusInvariants(t, desktopSafe, desktopWrapped)
			assertCorpusLayoutInvariants(t, desktopWrapped)

			goldenPath := strings.TrimSuffix(p, ".eml") + ".golden.html"
			if update {
				if err := os.WriteFile(goldenPath, []byte(wrapped), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden (run with URMAIL_UPDATE_GOLDEN=1 to create): %v", err)
			}
			if string(want) != wrapped {
				t.Fatalf("rendered output drifted from golden %s (size %d vs %d); regenerate with URMAIL_UPDATE_GOLDEN=1 after a reviewed pipeline change",
					goldenPath, len(wrapped), len(want))
			}
		})
	}
}

// corpusSanity guards the fixture helpers themselves against silent drift.
func TestCorpusFixtureSanity(t *testing.T) {
	raw := []byte("Content-Type: text/html; charset=utf-8\r\n\r\n<p>hi</p>")
	if got := corpusHTMLPart(t, raw); got != "<p>hi</p>" {
		t.Fatalf("simple html part extracted as %q", got)
	}
}

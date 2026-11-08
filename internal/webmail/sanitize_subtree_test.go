package webmail

import (
	"strings"
	"testing"
)

// TestSanitizeHTMLDropsDisallowedSubtrees guards the parser-level subtree
// drop: an element that must be removed is removed from the document even
// when it sits below a kept ancestor. The first version of the sanitizer
// only filtered the top level of the body and silently *kept* nested
// script/style/iframe/form/comment nodes (and blocked images with their src
// stripped), while still reporting active_content_removed.
func TestSanitizeHTMLDropsDisallowedSubtrees(t *testing.T) {
	cases := []struct {
		name string
		in   string
		glut []string // substrings that must not survive anywhere
		note string   // note the pass must report
		keep string   // content that must survive
	}{
		{
			name: "script inside a table cell",
			in:   `<table><tr><td><script>alert(1)</script><p>ok</p></td></tr></table>`,
			glut: []string{"<script", "alert(1)"},
			note: NoteActiveContentRemoved,
			keep: "<p>ok</p>",
		},
		{
			name: "style inside a div",
			in:   `<div><table><tr><td><style>p{color:red}</style></td></tr></table></div>`,
			glut: []string{"<style", "color:red"},
			note: NoteActiveContentRemoved,
		},
		{
			name: "iframe inside a cell",
			in:   `<table><tr><td><iframe src="https://evil.example"></iframe></td></tr></table>`,
			glut: []string{"<iframe", "evil.example"},
			note: NoteActiveContentRemoved,
		},
		{
			name: "form with controls inside a cell",
			in:   `<table><tr><td><form action="https://evil.example"><input name="pw"></form></td></tr></table>`,
			glut: []string{"<form", "<input", "evil.example"},
			note: NoteActiveContentRemoved,
		},
		{
			name: "comment inside a cell",
			in:   `<table><tr><td><!-- secret --><p>ok</p></td></tr></table>`,
			glut: []string{"secret", "<!--"},
			keep: "<p>ok</p>",
		},
		{
			name: "deeply nested video with poster",
			in:   `<div><table><tr><td><div><video poster="https://evil.example/x.jpg"></video></div></td></tr></table></div>`,
			glut: []string{"<video", "poster", "evil.example"},
			note: NoteActiveContentRemoved,
		},
	}
	for _, tc := range cases {
		out, notes := SanitizeHTML(tc.in)
		for _, banned := range tc.glut {
			if strings.Contains(out, banned) {
				t.Fatalf("%s: %q survived in %q", tc.name, banned, out)
			}
		}
		if tc.note != "" && !hasNote(notes, tc.note) {
			t.Fatalf("%s: missing note %s: %v", tc.name, tc.note, notes)
		}
		if tc.keep != "" && !strings.Contains(out, tc.keep) {
			t.Fatalf("%s: lost %q in %q", tc.name, tc.keep, out)
		}
	}
}

// TestSanitizeHTMLDropsBlockedImagesEverywhere pins the privacy contract for
// blocked remote images: the whole img element disappears, at every depth.
// A residue without src rendered as a broken-image placeholder in the
// reading pane (the "lone icon" production defect) and left an element the
// client could mistake for content.
func TestSanitizeHTMLDropsBlockedImagesEverywhere(t *testing.T) {
	in := `<table><tr>` +
		`<td><img src="https://a.example/icon.png" alt="icon"></td>` +
		`<td><div><img src="https://a.example/banner.gif" style="width:100%;height:auto" alt="banner"></div></td>` +
		`</tr></table>`
	out, notes := SanitizeHTML(in)
	if strings.Contains(out, "<img") {
		t.Fatalf("blocked images must be dropped at every depth: %q", out)
	}
	if !hasNote(notes, NoteRemoteImagesBlocked) {
		t.Fatalf("missing blocked-images note: %v", notes)
	}
}

// TestSanitizeHTMLKeepsBodyLevelText covers text that the HTML parser places
// directly into the body (foster-parented from a stray table cell, or a
// plain line between top-level tags). Dropping those nodes used to lose
// real content.
func TestSanitizeHTMLKeepsBodyLevelText(t *testing.T) {
	out, _ := SanitizeHTML(`<p>a</p>bare line<p>c</p>`)
	if !strings.Contains(out, "bare line") {
		t.Fatalf("body-level text lost: %q", out)
	}
}

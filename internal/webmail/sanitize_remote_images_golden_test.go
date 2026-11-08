package webmail

import "testing"

// TestSanitizeRemoteImagesGolden is the server-side counterpart of the client
// contract for remote images: one identical input, two states (default and
// AllowRemoteImages opt-in), compared as exact golden output. Absolute
// http(s) <img src> survives only the opt-in pass; protocol-relative img src,
// srcset, remote poster/background, CSS url(), scripts and dangerous schemes
// are removed in both states; safe raster data: images survive; cid images
// are only rewritten when a rewriter is configured. The sanitizer source must
// not be loosened to keep this test green.
func TestSanitizeRemoteImagesGolden(t *testing.T) {
	const in = "<img src=\"https://track.example/pixel.gif\" alt=\"pixel\">" +
		"<img src=\"http://track.example/pixel.gif\">" +
		"<img src=\"//evil.example/x.png\">" +
		"<img src=\"a.png\" srcset=\"https://track.example/x 2x\">" +
		"<td background=\"https://evil.example/bg.png\">x</td>" +
		"<div style=\"background:url(https://evil.example/x.png)\">x</div>" +
		"<img src=\"data:image/svg+xml;base64,PHN2Zz4=\">" +
		"<img src=\"data:image/png;base64,iVBORw0KGgo=\" alt=\"dot\" onerror=\"alert(1)\">" +
		"<img src=\"javascript:alert(1)\">" +
		"<script>alert(1)</script>" +
		"<style>@import url(https://evil.example/x.css);</style>" +
		"<video poster=\"https://evil.example/x.jpg\"></video>"

	// The stray <td> above is foster-parented by the HTML parser, so its
	// text lands directly in the body and is preserved as bare text
	// (dropping it used to lose real content).
	const blockedGolden = "x<div>x</div><img alt=\"dot\" src=\"data:image/png;base64,iVBORw0KGgo=\"/>"
	out, notes := SanitizeHTML(in)
	if out != blockedGolden {
		t.Fatalf("default pass mismatch:\n got %q\nwant %q", out, blockedGolden)
	}
	for _, want := range []string{NoteRemoteImagesBlocked, NoteCIDUnresolved, NoteUnsafeStyleRemoved, NoteActiveContentRemoved} {
		if !hasNote(notes, want) {
			t.Fatalf("default pass missing note %s: %v", want, notes)
		}
	}

	const allowedGolden = "<img alt=\"pixel\" src=\"https://track.example/pixel.gif\"/>" +
		"<img src=\"http://track.example/pixel.gif\"/>" +
		"x<div>x</div>" +
		"<img alt=\"dot\" src=\"data:image/png;base64,iVBORw0KGgo=\"/>"
	allowed, allowedNotes := SanitizeHTMLOpts(in, SanitizeOptions{AllowRemoteImages: true})
	if allowed != allowedGolden {
		t.Fatalf("opt-in pass mismatch:\n got %q\nwant %q", allowed, allowedGolden)
	}
	if hasNote(allowedNotes, NoteRemoteImagesBlocked) {
		t.Fatalf("opt-in pass must not report blocked remote images: %v", allowedNotes)
	}
	for _, banned := range []string{"//evil.example", "srcset", "background=", "url(", "poster", "script", "javascript:", "image/svg"} {
		if containsFold(allowed, banned) {
			t.Fatalf("opt-in output contains %q: %s", banned, allowed)
		}
	}

	// cid only becomes renderable when a configured rewriter maps it.
	rewritten, cidNotes := SanitizeHTMLOpts("<img src=\"cid:part1@example\" alt=\"logo\">",
		SanitizeOptions{AllowRemoteImages: true, CIDRewriter: func(cid string) (string, bool) { return "/att/" + cid, cid == "part1@example" }})
	if rewritten != "<img alt=\"logo\" src=\"/att/part1@example\"/>" {
		t.Fatalf("cid rewrite mismatch: %q", rewritten)
	}
	if len(cidNotes) != 0 {
		t.Fatalf("rewritten cid must not produce notes: %v", cidNotes)
	}
	unresolved, notesNoRewriter := SanitizeHTML("<img src=\"cid:part1@example\">")
	if containsFold(unresolved, "cid:") || !hasNote(notesNoRewriter, NoteCIDUnresolved) {
		t.Fatalf("cid without rewriter must drop and note: %q %v", unresolved, notesNoRewriter)
	}
}

func containsFold(s, sub string) bool {
	rs := []rune(s)
	ru := []rune(sub)
	for i := 0; i+len(ru) <= len(rs); i++ {
		match := true
		for j := range ru {
			lower := func(r rune) rune {
				if r >= 'A' && r <= 'Z' {
					return r + 32
				}
				return r
			}
			if lower(rs[i+j]) != lower(ru[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

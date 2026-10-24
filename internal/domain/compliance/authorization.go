package compliance

import (
	"errors"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// Supported content access scopes.
const (
	ScopeMetadataOnly = "metadata_only"
	ScopeFullContent  = "full_content"
	ScopeExport       = "export"
)

// Tenant roles that may hold and exercise content grants. The values match the
// user_tenant_roles.role CHECK constraint of the database schema
// ('owner', 'admin', 'billing', 'member').
//
// These are tenant roles, not PostgreSQL roles: the database roles live in
// internal/db (urmail_app / urmail_platform) and are unrelated.
const (
	TenantRoleOwner   = "owner"
	TenantRoleAdmin   = "admin"
	TenantRoleBilling = "billing"
	TenantRoleMember  = "member"
)

// Compliance authorization errors.
var (
	ErrNoGrant           = errors.New("no compliance content grant found")
	ErrGrantRevoked      = errors.New("compliance grant has been revoked")
	ErrGrantExpired      = errors.New("compliance grant has expired")
	ErrInsufficientScope = errors.New("insufficient compliance content scope")
	// ErrRoleNotPermitted is returned when the tenant role of the requesting
	// user (or of the granter) is not allowed to hold or exercise the scope of
	// a content grant.
	ErrRoleNotPermitted = errors.New("tenant role may not hold or exercise this content grant")
)

// ContentGrant represents an explicit authorization grant allowing a user to read or export archive content.
//
// Provenance (M5-M7 review v2): a grant records who issued it (GrantedBy) and,
// once it is revoked, who revoked it and when (RevokedBy / RevokedAt). Those
// fields are additive: the original fields keep their meaning and the original
// behaviour of AuthorizeContentAccess is unchanged.
type ContentGrant struct {
	ID        int64
	TenantID  int64
	UserID    int64
	Scope     string
	ExpiresAt *time.Time
	Revoked   bool
	CreatedAt time.Time

	// GrantedBy is the user id of the actor that issued the grant. Zero means
	// the grant has no recorded issuer (a legacy row) and must be treated as
	// unattributed by the caller.
	GrantedBy int64
	// RevokedBy is the user id of the actor that revoked the grant. It is nil
	// while the grant is live.
	RevokedBy *int64
	// RevokedAt is the time the grant was revoked. It is nil while the grant
	// is live.
	RevokedAt *time.Time
}

// Revoke marks the grant as revoked and records who revoked it and when.
// A zero time is replaced by the current time so a revoked grant always
// carries a revocation timestamp.
func (g *ContentGrant) Revoke(actorID int64, at time.Time) {
	if g == nil {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	g.Revoked = true
	g.RevokedBy = &actorID
	g.RevokedAt = &at
}

// AuthorizeContentAccess verifies if the given grant authorizes the requested scope at now.
//
// The behaviour of this function is unchanged by the role gate added later:
// it only looks at the grant itself. Handlers that serve tenant users must
// additionally apply the role gate, either with AuthorizeContentAccessWithRole
// or with RoleMayHoldScope.
func AuthorizeContentAccess(grant *ContentGrant, requiredScope string, now time.Time) error {
	if grant == nil {
		return ErrNoGrant
	}
	if grant.Revoked {
		return ErrGrantRevoked
	}
	if now.IsZero() {
		now = time.Now()
	}
	if grant.ExpiresAt != nil && now.After(*grant.ExpiresAt) {
		return ErrGrantExpired
	}

	switch requiredScope {
	case ScopeMetadataOnly:
		return nil
	case ScopeFullContent:
		if grant.Scope == ScopeFullContent || grant.Scope == ScopeExport {
			return nil
		}
		return ErrInsufficientScope
	case ScopeExport:
		if grant.Scope == ScopeExport {
			return nil
		}
		return ErrInsufficientScope
	default:
		return ErrInsufficientScope
	}
}

// RoleMayHoldScope reports whether a tenant role may hold and exercise a
// content grant of the given scope.
//
// Only an owner or an admin of the tenant may hold, read or exercise a grant
// that exposes message content (full_content) or an archive export (export).
// A plain member may only hold a metadata-only grant, which exposes no
// message content. Unknown roles and unknown scopes are denied, so the check
// fails closed.
func RoleMayHoldScope(role string, scope string) bool {
	switch scope {
	case ScopeMetadataOnly:
		switch role {
		case TenantRoleOwner, TenantRoleAdmin, TenantRoleBilling, TenantRoleMember:
			return true
		}
		return false
	case ScopeFullContent, ScopeExport:
		switch role {
		case TenantRoleOwner, TenantRoleAdmin:
			return true
		}
		return false
	default:
		return false
	}
}

// AuthorizeContentAccessWithRole applies every check of AuthorizeContentAccess
// and additionally requires the requesting user's tenant role to be allowed to
// hold and exercise the grant.
//
// This is the function tenant-facing handlers should call: a tenant member
// must never be able to read or export message content, not even when a grant
// row exists for them. The grant checks run first, so a revoked or expired
// grant still reports its own reason.
func AuthorizeContentAccessWithRole(grant *ContentGrant, requiredScope string, requesterRole string, now time.Time) error {
	if err := AuthorizeContentAccess(grant, requiredScope, now); err != nil {
		return err
	}
	if grant == nil {
		// Unreachable: AuthorizeContentAccess already rejected a nil grant.
		return ErrNoGrant
	}
	if !RoleMayHoldScope(requesterRole, grant.Scope) {
		return ErrRoleNotPermitted
	}
	if !RoleMayHoldScope(requesterRole, requiredScope) {
		return ErrRoleNotPermitted
	}
	return nil
}

// AuthorizeGrantIssuance reports whether a tenant role may issue a content
// grant of the given scope inside its tenant.
//
// Grant issuance is the second half of the role gate: a member must not be
// able to grant itself an export or content grant, nor grant one to anybody
// else. The issued grant must record the granter in ContentGrant.GrantedBy.
func AuthorizeGrantIssuance(granterRole string, scope string) error {
	if !RoleMayHoldScope(granterRole, scope) {
		return ErrRoleNotPermitted
	}
	return nil
}

// allowedTags maps HTML tags that are safe to keep in a preview to the set of
// attribute names that may survive sanitization for that tag.
var allowedTags = map[string]map[string]bool{
	"p":          {},
	"br":         {},
	"b":          {},
	"i":          {},
	"u":          {},
	"s":          {},
	"strong":     {},
	"em":         {},
	"code":       {},
	"pre":        {},
	"blockquote": {},
	"hr":         {},
	"ul":         {},
	"ol":         {},
	"li":         {},
	"h1":         {},
	"h2":         {},
	"h3":         {},
	"h4":         {},
	"h5":         {},
	"h6":         {},
	"span":       {},
	"div":        {},
	"table":      {"border": true, "cellpadding": true, "cellspacing": true},
	"thead":      {},
	"tbody":      {},
	"tfoot":      {},
	"tr":         {},
	"td":         {"colspan": true, "rowspan": true},
	"th":         {"colspan": true, "rowspan": true},
	"a":          {"href": true, "title": true, "target": true, "rel": true},
	"img":        {"src": true, "alt": true, "title": true, "width": true, "height": true},
}

// dropContentTags are removed together with their entire subtree: their
// content is never user-visible text and may hide payloads.
var dropContentTags = map[string]bool{
	"script":   true,
	"style":    true,
	"iframe":   true,
	"object":   true,
	"embed":    true,
	"noscript": true,
	"template": true,
	"svg":      true,
	"math":     true,
	"head":     true,
	"title":    true,
}

const blockedImagePlaceholder = "data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg'/>"

// safeURL reports whether the URL scheme is allowed in href/src attributes.
//
// Decision: only http and https are accepted. Previews are rendered in the
// tenant's own browser session, so every other scheme is either a script
// vector (javascript:, vbscript:), a content injection vector (data:,
// blob:, file:) or an unnecessary navigation privilege (mailto:, tel:).
// Relative URLs are rejected as well because the preview has no meaningful
// base URL and a relative href could resolve to an unexpected origin.
//
// Control characters are stripped first, so obfuscations such as
// "java\tscript:alert(1)" cannot smuggle a scheme past the check.
func safeURL(raw string) bool {
	var b strings.Builder
	for _, r := range raw {
		if r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	u := strings.ToLower(strings.TrimSpace(b.String()))
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

// Sanitizer limits.
//
// Both limits are documented and enforced. When either one is exceeded the
// sanitizer refuses to produce a preview: it returns HTMLPreviewUnavailableNotice
// together with a non-zero blocked count instead of silently truncating the
// document, because a truncated preview misrepresents the message it claims to
// show.
const (
	// MaxHTMLPreviewInputBytes is the largest HTML document, in bytes, that
	// SanitizeHTMLPreview will parse. Larger input is refused before parsing,
	// so a hostile or accidental multi-megabyte message cannot consume
	// unbounded CPU and memory in the preview path.
	MaxHTMLPreviewInputBytes = 1 << 20 // 1 MiB

	// MaxHTMLPreviewDepth is the deepest element nesting the sanitizer
	// accepts. The depth counts element nodes and includes the implicit html
	// and body wrappers (which sit at depth 1 and 2), so the limit bounds
	// both the sanitizer's own render recursion and the memory the parser
	// spends on a pathologically nested document.
	MaxHTMLPreviewDepth = 100

	// HTMLPreviewUnavailableNotice is returned verbatim, as plain text and
	// never as markup, when the input exceeds one of the limits above.
	// Callers that show it to a user should map it to a translated message.
	HTMLPreviewUnavailableNotice = "Preview unavailable: this message exceeds the preview limits."
)

// HTMLPreviewRefused reports whether the sanitizer refused to produce a
// preview because the input exceeded a documented limit.
func HTMLPreviewRefused(clean string) bool {
	return clean == HTMLPreviewUnavailableNotice
}

// SanitizeHTMLPreview parses rawHTML and rebuilds it from a strict tag and
// attribute whitelist, making it safe for in-browser preview: scripts,
// styles, frames, event handlers and non-http(s) URLs are removed, and
// remote image sources are neutralized into a local placeholder while the
// original URL is kept in data-blocked-src for diagnostics. The second
// return value counts the number of neutralized or blocked attributes. If
// parsing fails, the entire input is escaped to plain text.
//
// The input is bounded: a document larger than MaxHTMLPreviewInputBytes or
// nested deeper than MaxHTMLPreviewDepth is refused with
// HTMLPreviewUnavailableNotice and a non-zero blocked count, instead of being
// truncated into a preview that would misrepresent the message.
//
// Anchors that open a new browsing context (target="_blank") are given
// rel="noopener noreferrer" so the opened page cannot reach back into the
// preview through window.opener (reverse tabnabbing).
func SanitizeHTMLPreview(rawHTML string) (string, int) {
	if len(rawHTML) > MaxHTMLPreviewInputBytes {
		return HTMLPreviewUnavailableNotice, 1
	}
	doc, err := html.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return html.EscapeString(rawHTML), 0
	}
	if exceedsMaxDepth(doc, MaxHTMLPreviewDepth) {
		return HTMLPreviewUnavailableNotice, 1
	}

	var out strings.Builder
	blocked := 0
	var render func(n *html.Node)
	render = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			out.WriteString(html.EscapeString(n.Data))
			return
		case html.ElementNode:
			tag := strings.ToLower(n.Data)
			if dropContentTags[tag] {
				return
			}
			allowed := allowedTags[tag]
			var attrs []html.Attribute
			if allowed != nil {
				attrs, blocked = filterAttrs(tag, n.Attr, blocked)
			} else {
				// Unknown tags are unwrapped: their text children survive,
				// the tag and its attributes do not.
				blocked += len(n.Attr)
			}
			if allowed != nil {
				out.WriteString("<" + tag)
				for _, a := range attrs {
					out.WriteString(" " + a.Key + `="` + html.EscapeString(a.Val) + `"`)
				}
				out.WriteString(">")
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				render(c)
			}
			if allowed != nil && !isVoidTag(tag) {
				out.WriteString("</" + tag + ">")
			}
		default:
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				render(c)
			}
		}
	}
	render(doc)

	return strings.TrimSpace(out.String()), blocked
}

// exceedsMaxDepth reports whether the element nesting under root is deeper
// than max. The depth counts element nodes and includes the implicit html and
// body wrappers.
//
// The walk is iterative on purpose: a pathologically nested document must not
// be able to overflow the goroutine stack of the check itself.
func exceedsMaxDepth(root *html.Node, max int) bool {
	type frame struct {
		node  *html.Node
		depth int
	}
	stack := []frame{{node: root, depth: 0}}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		depth := f.depth
		if f.node.Type == html.ElementNode {
			depth++
			if depth > max {
				return true
			}
		}
		for c := f.node.FirstChild; c != nil; c = c.NextSibling {
			stack = append(stack, frame{node: c, depth: depth})
		}
	}
	return false
}

func isVoidTag(tag string) bool {
	switch tag {
	case "br", "img", "hr":
		return true
	}
	return false
}

// filterAttrs keeps only whitelisted attributes with safe values. Remote
// image sources are rewritten to a local placeholder; javascript: and
// data: URLs are dropped. Each dropped or neutralized attribute increments
// blocked.
//
// For anchors, an additional hardening is applied: an anchor that opens a new
// browsing context (target="_blank") always ends up with rel="noopener
// noreferrer", see hardenBlankTargetRel.
func filterAttrs(tag string, attrs []html.Attribute, blocked int) ([]html.Attribute, int) {
	allowed := allowedTags[tag]
	kept := make([]html.Attribute, 0, len(attrs))
	for _, a := range attrs {
		key := strings.ToLower(a.Key)
		if !allowed[key] {
			// Drops on*, srcset, style and anything else not whitelisted.
			blocked++
			continue
		}
		switch {
		case key == "href":
			if !safeURL(a.Val) {
				blocked++
				continue
			}
		case key == "src" && tag == "img":
			if !safeURL(a.Val) {
				blocked++
				continue
			}
			// Neutralize remote tracking pixels: keep the original URL in
			// data-blocked-src but point src at a local placeholder.
			kept = append(kept, html.Attribute{Namespace: a.Namespace, Key: "data-blocked-src", Val: a.Val})
			a.Val = blockedImagePlaceholder
			blocked++
		}
		kept = append(kept, html.Attribute{Namespace: a.Namespace, Key: key, Val: a.Val})
	}
	if tag == "a" {
		kept = hardenBlankTargetRel(kept)
	}
	return kept, blocked
}

// hardenBlankTargetRel forces rel="noopener noreferrer" on anchors that open
// a new browsing context (target="_blank").
//
// Without it the opened page keeps a handle on the preview through
// window.opener and can navigate it to a phishing page (reverse tabnabbing).
// The keywords are added only when missing; every other rel token the author
// set is preserved. Repeated rel attributes are merged into one, and a
// contradictory rel="opener" is neutralised because the HTML specification
// gives "noopener" precedence over "opener".
func hardenBlankTargetRel(attrs []html.Attribute) []html.Attribute {
	opensNewContext := false
	for _, a := range attrs {
		if a.Key == "target" && strings.EqualFold(strings.TrimSpace(a.Val), "_blank") {
			opensNewContext = true
			break
		}
	}
	if !opensNewContext {
		return attrs
	}

	out := make([]html.Attribute, 0, len(attrs)+1)
	tokens := make([]string, 0, 4)
	seen := make(map[string]bool, 4)
	relPos := -1
	for _, a := range attrs {
		if a.Key != "rel" {
			out = append(out, a)
			continue
		}
		if relPos < 0 {
			relPos = len(out)
		}
		for _, tok := range strings.Fields(a.Val) {
			lower := strings.ToLower(tok)
			if seen[lower] {
				continue
			}
			seen[lower] = true
			tokens = append(tokens, tok)
		}
	}
	for _, need := range []string{"noopener", "noreferrer"} {
		if seen[need] {
			continue
		}
		seen[need] = true
		tokens = append(tokens, need)
	}
	rel := html.Attribute{Key: "rel", Val: strings.Join(tokens, " ")}
	if relPos < 0 {
		out = append(out, rel)
		return out
	}
	out = append(out, html.Attribute{})
	copy(out[relPos+1:], out[relPos:])
	out[relPos] = rel
	return out
}

package mail

import (
	"strings"
	"unicode"
)

// Parties are the addresses already visible on a message. Bcc is intentionally
// absent: it must never be added to a reply.
type Parties struct {
	From    []string
	ReplyTo []string
	To      []string
	Cc      []string
	Self    []string
}

// Reply selects Reply-To when present, otherwise From. It never includes Bcc.
func Reply(p Parties) []string {
	source := p.ReplyTo
	if len(clean(source)) == 0 {
		source = p.From
	}
	return first(excludeSelf(clean(source), p.Self))
}

// ReplyAll keeps every visible participant except the sender's own addresses.
// Reply-To replaces From. Bcc is not a field on Parties and is not recovered.
func ReplyAll(p Parties) (to, cc []string) {
	self := p.Self
	authors := p.ReplyTo
	if len(clean(authors)) == 0 {
		authors = p.From
	}
	to = excludeSelf(clean(authors), self)
	if len(to) == 0 {
		to = excludeSelf(clean(p.To), self)
	}
	seen := map[string]struct{}{}
	for _, addr := range to {
		seen[strings.ToLower(addr)] = struct{}{}
	}
	for _, addr := range excludeSelf(append(clean(p.To), clean(p.Cc)...), self) {
		key := strings.ToLower(addr)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		cc = append(cc, addr)
	}
	return to, cc
}

func first(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return in[:1]
}

func clean(in []string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, addr := range in {
		addr = strings.TrimSpace(addr)
		if addr == "" || strings.ContainsAny(addr, "\r\n") {
			continue
		}
		key := strings.ToLower(addr)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, addr)
	}
	return out
}

func excludeSelf(in, self []string) []string {
	own := map[string]struct{}{}
	for _, addr := range self {
		own[strings.ToLower(strings.TrimSpace(addr))] = struct{}{}
	}
	var out []string
	for _, addr := range in {
		if _, ok := own[strings.ToLower(addr)]; ok {
			continue
		}
		if strings.IndexFunc(addr, unicode.IsControl) >= 0 {
			continue
		}
		out = append(out, addr)
	}
	return out
}

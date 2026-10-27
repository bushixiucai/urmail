package webmail

import (
	"fmt"
	"strconv"
	"strings"
)

// NormalizeThreadSubject strips reply/forward markers and surrounding
// whitespace for the subject-normalization thread fallback (RFC 5256 base
// subject approximation, extended with common CJK markers).
func NormalizeThreadSubject(s string) string {
	s = strings.TrimSpace(s)
	for {
		prev := s
		lower := strings.ToLower(s)
		for _, prefix := range []string{"re:", "fw:", "fwd:", "回复:", "转发:", "转发："} {
			if strings.HasPrefix(lower, prefix) {
				s = strings.TrimSpace(s[len(prefix):])
				break
			}
		}
		if s == prev {
			break
		}
	}
	return s
}

// BuildThreads maps each UID to its thread root using
// References/In-Reply-To/Message-ID chains first and normalized subject as
// fallback. This is the "client" threading path used whenever the IMAP
// server does not advertise THREAD=REFERENCES. Group roots are the lowest
// UID in the group so thread_id stays stable across pages.
func BuildThreads(msgs []MessageSummary) map[uint32]uint32 {
	if len(msgs) == 0 {
		return nil
	}
	ordered := make([]MessageSummary, len(msgs))
	copy(ordered, msgs)
	sortByUID(ordered)
	idToUID := make(map[string]uint32)
	for _, m := range ordered {
		if mid := strings.Trim(m.MessageID, "<>"); mid != "" {
			idToUID[mid] = m.UID
		}
	}
	parent := make(map[uint32]uint32, len(ordered))
	for _, m := range ordered {
		parent[m.UID] = m.UID
	}
	var find func(uint32) uint32 //nolint:staticcheck // S1021: recursive closure must be declared before assignment
	find = func(u uint32) uint32 {
		for parent[u] != u {
			parent[u] = parent[parent[u]]
			u = parent[u]
		}
		return u
	}
	union := func(a, b uint32) {
		ra, rb := find(a), find(b)
		if ra == rb {
			return
		}
		if ra < rb {
			parent[rb] = ra
		} else {
			parent[ra] = rb
		}
	}
	// 1) Reply chains.
	for _, m := range ordered {
		self := find(m.UID)
		if ref := strings.Trim(m.InReplyTo, "<>"); ref != "" {
			if target, ok := idToUID[ref]; ok {
				union(self, find(target))
			}
		}
	}
	// 2) Subject fallback: group only messages sharing a non-empty
	// normalized subject.
	bySubject := make(map[string]uint32)
	for _, m := range ordered {
		norm := strings.ToLower(NormalizeThreadSubject(m.Subject))
		if norm == "" {
			continue
		}
		if first, ok := bySubject[norm]; ok {
			union(find(m.UID), find(first))
		} else {
			bySubject[norm] = m.UID
		}
	}
	out := make(map[uint32]uint32, len(ordered))
	for _, m := range ordered {
		out[m.UID] = find(m.UID)
	}
	return out
}

func sortByUID(msgs []MessageSummary) {
	for i := 1; i < len(msgs); i++ {
		for j := i; j > 0 && msgs[j].UID < msgs[j-1].UID; j-- {
			msgs[j], msgs[j-1] = msgs[j-1], msgs[j]
		}
	}
}

// ApplyThreads stamps ThreadID/ThreadCount on msgs using the given mapping.
// The count covers the summaries passed in (the loaded set), not the whole
// folder — a documented limitation of the client threading path.
func ApplyThreads(msgs []MessageSummary, threads map[uint32]uint32) {
	counts := make(map[uint32]int)
	for _, root := range threads {
		counts[root]++
	}
	for i := range msgs {
		if root, ok := threads[msgs[i].UID]; ok {
			msgs[i].ThreadID = root
			msgs[i].ThreadCount = counts[root]
		}
	}
}

// parseThreadRoots parses the nested parens of a concatenated "* THREAD"
// response into (uid -> root uid) pairs. Each top-level element is one
// thread; its root is the first UID inside the element.
func parseThreadRoots(s string) (map[uint32]uint32, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil //nolint:nilnil // empty THREAD response means no roots
	}
	if !strings.HasPrefix(s, "(") {
		return nil, fmt.Errorf("bad THREAD response")
	}
	out := make(map[uint32]uint32)
	depth := 0
	var cur strings.Builder
	firstInThread := uint32(0)
	var pending []uint32
	flushUID := func() {
		if cur.Len() == 0 {
			return
		}
		n, err := strconv.ParseUint(cur.String(), 10, 32)
		cur.Reset()
		if err != nil {
			return
		}
		uid := uint32(n)
		pending = append(pending, uid)
		if firstInThread == 0 {
			firstInThread = uid
		}
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
			if depth == 1 {
				firstInThread = 0
				pending = pending[:0]
			}
		case ')':
			flushUID()
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("bad THREAD response")
			}
			if depth == 0 {
				for _, uid := range pending {
					out[uid] = firstInThread
				}
			}
		case ' ':
			flushUID()
		default:
			cur.WriteByte(s[i])
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("unbalanced THREAD response")
	}
	return out, nil
}

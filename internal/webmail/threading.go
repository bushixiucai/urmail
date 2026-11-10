package webmail

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SubjectFallbackWindow bounds how far apart two messages may be and still be
// merged by the normalized-subject fallback. Conversations that are further
// apart in time are treated as unrelated even when the subject matches.
const SubjectFallbackWindow = 7 * 24 * time.Hour

// WeakAggregationThreshold bounds hard thread aggregation. Threads with more
// members are stamped WeakAggregation=true and EarlierCount carries how many
// members fall outside the newest window the UI aggregates (Tiger decision
// v0.6.20 C1: giant threads render as newest-20 plus an "earlier" summary
// bar instead of one unbounded stack).
const WeakAggregationThreshold = 20

// systemSenderMarkers lists the identifying fragments of automated system
// senders (bounce daemons, verifiers, auto-responders, postmasters). Mail
// from these senders never joins the subject fallback, so daily notices and
// bounces cannot grow phantom threads (THREAD-UX-REVIEW F1; QA F-2). Reply
// chains still group them: References/In-Reply-To is the only strong
// grouping.
var systemSenderMarkers = []string{
	"mailer-daemon",
	"noreply",
	"no-reply",
	"no_reply",
	"donotreply",
	"do-not-reply",
	"do_not_reply",
	"auto-reply",
	"autoreply",
	"auto_reply",
	"autoresponder",
	"auto-responder",
	"postmaster",
}

// isSystemSender reports whether the address belongs to an automated system
// sender. The check covers the email AND the display name: garbled senders
// sometimes survive as display-only addresses with an empty email (QA F-2),
// and the marker then lives in the name.
func isSystemSender(addr Address) bool {
	email := strings.ToLower(strings.TrimSpace(addr.Email))
	name := strings.ToLower(strings.TrimSpace(addr.Name))
	for _, marker := range systemSenderMarkers {
		if strings.Contains(email, marker) || strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

// hasNonSystemSender reports whether any sender of m is a human-operated
// mailbox. The subject fallback only considers groups with at least one.
func hasNonSystemSender(m MessageSummary) bool {
	for _, addr := range m.From {
		if !isSystemSender(addr) {
			return true
		}
	}
	return false
}

// groupHasNonSystemSender reports whether any member of the group has a
// non-system sender.
func groupHasNonSystemSender(group []MessageSummary) bool {
	for _, m := range group {
		if hasNonSystemSender(m) {
			return true
		}
	}
	return false
}

// Thread scopes report how complete the stamped thread counts are.
//
//   - ThreadScopeServer: the counts come from the IMAP THREAD response, which
//     carries every member of every thread (exact folder-wide counts).
//   - ThreadScopeFolder: the client path counted every message of the
//     filtered set (exact, but only for sets that fit the scan bound).
//   - ThreadScopeBounded: the client path could only count the scanned
//     prefix (MaxSortScan), so the counts are a lower bound.
const (
	ThreadScopeServer  = "server"
	ThreadScopeFolder  = "folder"
	ThreadScopeBounded = "bounded"
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

// threadParticipants returns the addresses that identify the people involved
// in a message: the sender plus every visible recipient (ENVELOPE To/Cc).
// Addresses compare case-insensitively.
func threadParticipants(m MessageSummary) map[string]struct{} {
	count := len(m.From) + len(m.To) + len(m.Cc)
	if count == 0 {
		return nil
	}
	out := make(map[string]struct{}, count)
	add := func(addrs []Address) {
		for _, addr := range addrs {
			email := strings.ToLower(strings.TrimSpace(addr.Email))
			if email == "" {
				continue
			}
			out[email] = struct{}{}
		}
	}
	add(m.From)
	add(m.To)
	add(m.Cc)
	return out
}

// threadSenders returns only the sender addresses of m.
func threadSenders(m MessageSummary) map[string]struct{} {
	out := make(map[string]struct{}, len(m.From))
	for _, addr := range m.From {
		email := strings.ToLower(strings.TrimSpace(addr.Email))
		if email == "" {
			continue
		}
		out[email] = struct{}{}
	}
	return out
}

// sharesNonSender reports whether m shares an address with the cluster that
// is not merely the identical sender on both sides. Pure same-sender overlap
// is not conversation evidence (Tiger decision v0.6.20 C1): broadcast mail
// from one automated or repetitive sender must not merge by subject, while a
// shared recipient (a reply whose References were lost) still merges.
func sharesNonSender(clusterParticipants, clusterSenders map[string]struct{}, m MessageSummary) bool {
	senders := threadSenders(m)
	for key := range threadParticipants(m) {
		if _, ok := clusterParticipants[key]; !ok {
			continue
		}
		_, mSends := senders[key]
		_, cSends := clusterSenders[key]
		if mSends && cSends {
			continue // same address on both sender sides: no evidence
		}
		return true
	}
	return false
}

// withinFallbackWindow reports whether two dated messages are close enough in
// time for the subject fallback. A missing date never matches: an unknown
// timestamp must not widen the fallback.
func withinFallbackWindow(a, b *time.Time) bool {
	if a == nil || b == nil {
		return false
	}
	diff := a.Sub(*b)
	if diff < 0 {
		diff = -diff
	}
	return diff <= SubjectFallbackWindow
}

// threadCluster aggregates the fallback evidence of one candidate group: the
// participants and senders seen in the group and the dates of its dated
// messages. Senders are kept apart so pure same-sender overlap can be
// rejected.
type threadCluster struct {
	root         uint32
	participants map[string]struct{}
	senders      map[string]struct{}
	dates        []*time.Time
}

// newThreadCluster builds the aggregate evidence of a message group.
func newThreadCluster(root uint32, group []MessageSummary) *threadCluster {
	cluster := &threadCluster{
		root:         root,
		participants: make(map[string]struct{}),
		senders:      make(map[string]struct{}),
	}
	for _, m := range group {
		for key := range threadParticipants(m) {
			cluster.participants[key] = struct{}{}
		}
		for key := range threadSenders(m) {
			cluster.senders[key] = struct{}{}
		}
		if m.Date != nil {
			cluster.dates = append(cluster.dates, m.Date)
		}
	}
	return cluster
}

// compatibleWith reports whether the given message group may join the
// cluster under the tightened subject fallback. Two conditions qualify: a
// shared address that is not pure same-sender overlap, or the two sides
// being at most SubjectFallbackWindow apart in time.
func (c *threadCluster) compatibleWith(group []MessageSummary) bool {
	for _, m := range group {
		if sharesNonSender(c.participants, c.senders, m) {
			return true
		}
		for _, d := range c.dates {
			if withinFallbackWindow(d, m.Date) {
				return true
			}
		}
	}
	return false
}

// absorb folds another group's evidence into the cluster.
func (c *threadCluster) absorb(other *threadCluster) {
	for key := range other.participants {
		c.participants[key] = struct{}{}
	}
	c.dates = append(c.dates, other.dates...)
}

// BuildThreads maps each UID to its thread root using
// References/In-Reply-To/Message-ID chains first and normalized subject as
// fallback. This is the "client" threading path used whenever the IMAP
// server does not advertise THREAD=REFERENCES. Group roots are the lowest
// UID in the group so thread_id stays stable across pages.
//
// The reply chains are the only strong grouping: In-Reply-To joins the
// immediate parent and References walks the full ancestor chain (some
// clients and bounce daemons fill References without In-Reply-To).
//
// The subject fallback is deliberately narrow. Groups whose senders are all
// automated system mail (bounce daemons, verifiers, auto-responders) never
// join it, so daily notices cannot grow phantom threads. For the remaining
// groups, messages sharing a normalized subject merge only when they look
// like the same conversation — a shared address that is not the identical
// sender on both sides, or at most SubjectFallbackWindow between them. A
// missing Date never merges on subject alone. Reply chains are never split
// by the fallback: it compares whole chain groups, so a chain always stays
// one thread.
func BuildThreads(msgs []MessageSummary) map[uint32]uint32 {
	return buildThreads(msgs, false)
}

// BuildThreadsFolderScoped behaves like BuildThreads, but the subject
// fallback only merges groups inside the same folder. The cross-folder
// thread-members path uses it: References chains still span folders (the
// only strong grouping), while bounce sweeps and broadcasts must not fuse
// INBOX and Sent members by subject.
func BuildThreadsFolderScoped(msgs []MessageSummary) map[uint32]uint32 {
	return buildThreads(msgs, true)
}

func buildThreads(msgs []MessageSummary, perFolderFallback bool) map[uint32]uint32 {
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
	// 1) Reply chains: the only strong grouping.
	for _, m := range ordered {
		self := find(m.UID)
		if ref := strings.Trim(m.InReplyTo, "<>"); ref != "" {
			if target, ok := idToUID[ref]; ok {
				union(self, find(target))
			}
		}
		for _, ref := range m.References {
			ref = strings.Trim(ref, "<>")
			if target, ok := idToUID[ref]; ok && target != m.UID {
				union(self, find(target))
			}
		}
	}
	// 2) Subject fallback. The reply chains above already decided which
	// messages belong together, so the fallback works on whole groups: a
	// chain is never split, and a group is compared as a unit.
	groups := make(map[uint32][]MessageSummary, len(ordered))
	for _, m := range ordered {
		root := find(m.UID)
		groups[root] = append(groups[root], m)
	}
	buckets := make(map[string][]uint32)
	seen := make(map[string]map[uint32]bool)
	for _, m := range ordered {
		norm := strings.ToLower(NormalizeThreadSubject(m.Subject))
		if norm == "" {
			continue
		}
		if perFolderFallback {
			// Folder-local fallback: the same subject in another folder is a
			// different bucket. Reply chains still span folders.
			norm = m.Folder + "\x00" + norm
		}
		root := find(m.UID)
		if seen[norm] == nil {
			seen[norm] = make(map[uint32]bool, 4)
		}
		if seen[norm][root] {
			continue
		}
		seen[norm][root] = true
		// Groups whose senders are all automated system mail never join the
		// fallback: bounce sweeps and daily notices must not merge.
		if !groupHasNonSystemSender(groups[root]) {
			continue
		}
		buckets[norm] = append(buckets[norm], root)
	}
	for _, bucket := range buckets {
		var clusters []*threadCluster
		for _, root := range bucket {
			group := groups[root]
			candidate := newThreadCluster(root, group)
			merged := false
			for _, cluster := range clusters {
				if !cluster.compatibleWith(group) {
					continue
				}
				union(root, cluster.root)
				cluster.absorb(candidate)
				merged = true
			}
			if !merged {
				clusters = append(clusters, candidate)
			}
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
// The count covers every UID in the mapping (the scanned set) and not only
// the messages in msgs, so a thread whose members are spread over several
// pages reports the same total on every page.
func ApplyThreads(msgs []MessageSummary, threads map[uint32]uint32) {
	counts := make(map[uint32]int)
	for _, root := range threads {
		counts[root]++
	}
	ApplyThreadsWithCounts(msgs, threads, counts)
}

// ApplyThreadsWithCounts stamps ThreadID/ThreadCount on msgs using the given
// root mapping and the per-root counts. The server path uses it with the
// member counts reported by the IMAP THREAD response: those count every
// member of the thread, not only the loaded page. Counts above
// WeakAggregationThreshold additionally stamp WeakAggregation and
// EarlierCount (the remainder beyond the newest-20 window).
func ApplyThreadsWithCounts(msgs []MessageSummary, threads map[uint32]uint32, counts map[uint32]int) {
	for i := range msgs {
		root, ok := threads[msgs[i].UID]
		if !ok {
			continue
		}
		msgs[i].ThreadID = root
		msgs[i].ThreadCount = counts[root]
		if counts[root] > WeakAggregationThreshold {
			msgs[i].WeakAggregation = true
			msgs[i].EarlierCount = counts[root] - WeakAggregationThreshold
		}
	}
}

// parseThreadRoots parses the nested parens of a concatenated "* THREAD"
// response into (uid -> root uid) pairs and the number of members in each
// thread. Each top-level element is one thread; its root is the first UID
// inside the element and its size is the number of UIDs the element holds —
// the server reports every member of the thread, so the count is
// folder-wide and not limited to the loaded page.
func parseThreadRoots(s string) (map[uint32]uint32, map[uint32]int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil, nil //nolint:nilnil // empty THREAD response means no roots
	}
	if !strings.HasPrefix(s, "(") {
		return nil, nil, fmt.Errorf("bad THREAD response")
	}
	out := make(map[uint32]uint32)
	counts := make(map[uint32]int)
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
				return nil, nil, fmt.Errorf("bad THREAD response")
			}
			if depth == 0 {
				for _, uid := range pending {
					out[uid] = firstInThread
				}
				if len(pending) > 0 {
					counts[firstInThread] = len(pending)
				}
			}
		case ' ':
			flushUID()
		default:
			cur.WriteByte(s[i])
		}
	}
	if depth != 0 {
		return nil, nil, fmt.Errorf("unbalanced THREAD response")
	}
	return out, counts, nil
}

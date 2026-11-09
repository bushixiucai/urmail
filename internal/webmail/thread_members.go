package webmail

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ThreadScanFolderBound caps how many newest summaries one folder contributes
// to the thread-member scan (contract §2: three folders, 3000 total worst
// case). Threads whose members fall out of every scanned prefix still work —
// they just report a bounded scope.
const ThreadScanFolderBound = 1000

// ThreadMembersOpts carries the thread-members query (contract
// Shared/design/THREAD-MEMBERS-API-CONTRACT.md):
//   - no anchor/before: the newest Limit members;
//   - AnchorUID: a limit-sized window containing that member of the anchor
//     folder — the anchor plus up to Limit-1 newer members, backfilled from
//     the older side when fewer newer members exist;
//   - Before: the opaque `oldest` cursor of the previous page; the next
//     strictly-older Limit members.
type ThreadMembersOpts struct {
	Limit     int
	AnchorUID uint32
	Before    string
}

// ClampThreadMembersOpts applies the contract defaults (limit 20, cap 50).
func ClampThreadMembersOpts(o ThreadMembersOpts) ThreadMembersOpts {
	if o.Limit <= 0 {
		o.Limit = 20
	}
	if o.Limit > 50 {
		o.Limit = 50
	}
	return o
}

// ThreadMembersResult is the cross-folder member list of one thread, ordered
// oldest → newest (the reading-pane stack order, so the client never
// re-sorts; ties keep (date, folder, uid) as returned).
type ThreadMembersResult struct {
	ThreadID     uint32           `json:"thread_id"`
	Total        int              `json:"total"`
	UnreadCount  int              `json:"unread_count"`
	Scope        string           `json:"scope,omitempty"`
	Messages     []MessageSummary `json:"messages"`
	HasMoreOlder bool             `json:"has_more_older"`
	Oldest       string           `json:"oldest,omitempty"`
	HasMoreNewer bool             `json:"has_more_newer"`
	NewerCount   int              `json:"newer_count"`
}

// ErrThreadMemberNotFound reports the anchor member is gone (404 at the API).
var ErrThreadMemberNotFound = fmt.Errorf("message not found")

// ErrInvalidThreadCursor reports an unusable `before` cursor (400).
var ErrInvalidThreadCursor = fmt.Errorf("invalid cursor")

// GetThreadMembers resolves the thread containing (folder, uid) across the
// anchor folder, Sent and INBOX, and returns one window of members.
//
// Grouping matches the listing path: References/In-Reply-To/Message-ID
// chains are the only strong grouping and they span folders; the subject
// fallback stays folder-local so bounce sweeps and broadcasts cannot fuse
// across folders. See THREAD-MEMBERS-API-CONTRACT.md.
func (c *imapConn) GetThreadMembers(ctx context.Context, folder string, uid uint32, opts ThreadMembersOpts) (ThreadMembersResult, error) {
	opts = ClampThreadMembersOpts(opts)
	if uid == 0 || strings.TrimSpace(folder) == "" {
		return ThreadMembersResult{}, fmt.Errorf("invalid uid")
	}
	scanFolders, err := c.threadScanFolders(ctx, folder)
	if err != nil {
		return ThreadMembersResult{}, err
	}
	bounded := false
	combined := make([]MessageSummary, 0, 64)
	for _, name := range scanFolders {
		if err := c.selectFolder(ctx, name); err != nil {
			if name == folder {
				return ThreadMembersResult{}, err
			}
			continue // a vanished auxiliary folder must not sink the query
		}
		uids, err := c.uidSearch(ctx, "")
		if err != nil {
			if name == folder {
				return ThreadMembersResult{}, err
			}
			continue
		}
		if len(uids) > ThreadScanFolderBound {
			uids = uids[len(uids)-ThreadScanFolderBound:] // newest N (ascending UIDs)
			bounded = true
		}
		sums, err := c.uidFetchSummaries(ctx, uids)
		if err != nil {
			if name == folder {
				return ThreadMembersResult{}, err
			}
			continue
		}
		for i := range sums {
			sums[i].Folder = name
			sums[i].FolderEnc = EncodeFolderName(name)
			sums[i].UIDValidity = c.selectedUIDValidity
		}
		combined = append(combined, sums...)
	}
	if len(combined) == 0 {
		return ThreadMembersResult{}, ErrThreadMemberNotFound
	}

	threads := BuildThreadsFolderScoped(combined)
	root, ok := threads[uid]
	if !ok {
		return ThreadMembersResult{}, ErrThreadMemberNotFound
	}
	counts := make(map[uint32]int, 8)
	for _, m := range combined {
		counts[threads[m.UID]]++
	}
	members := make([]MessageSummary, 0, counts[root])
	for _, m := range combined {
		if threads[m.UID] == root {
			members = append(members, m)
		}
	}
	sortThreadMembers(members)
	ApplyThreadsWithCounts(members, threads, counts)

	total := len(members)
	start, end, err := threadWindow(members, folder, uid, opts, total)
	if err != nil {
		return ThreadMembersResult{}, err
	}
	window := members[start:end]
	if window == nil {
		window = []MessageSummary{}
	}

	unread := 0
	for _, m := range members {
		if m.Folder == folder && !hasFlag(m.Flags, `\Seen`) {
			unread++
		}
	}

	out := ThreadMembersResult{
		ThreadID:     root,
		Total:        total,
		UnreadCount:  unread,
		Scope:        "folder",
		Messages:     window,
		HasMoreOlder: start > 0,
		HasMoreNewer: end < total,
		NewerCount:   total - end,
	}
	if bounded {
		out.Scope = "bounded"
	}
	if out.HasMoreOlder {
		out.Oldest = threadCursor(members[start].Folder, members[start].UID)
	}
	return out, nil
}

// threadScanFolders resolves the scan set: anchor folder first, then INBOX
// and the \Sent folder (deduplicated). Contract §2: the union never loses
// INBOX siblings when the anchor folder is Sent itself.
func (c *imapConn) threadScanFolders(ctx context.Context, anchor string) ([]string, error) {
	out := []string{anchor}
	seen := map[string]bool{anchor: true}
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	add("INBOX")
	folders, err := c.ListFolders(ctx)
	if err == nil {
		for _, f := range folders {
			if hasFlag(f.Flags, `\Sent`) {
				add(f.Name)
				break
			}
		}
	}
	return out, nil
}

// sortThreadMembers orders members by (date asc, folder, uid); a missing
// date sorts to the oldest end.
func sortThreadMembers(members []MessageSummary) {
	sort.SliceStable(members, func(i, j int) bool {
		a, b := members[i], members[j]
		adate, bdate := threadSortDate(a), threadSortDate(b)
		if !adate.Equal(bdate) {
			return adate.Before(bdate)
		}
		if a.Folder != b.Folder {
			return a.Folder < b.Folder
		}
		return a.UID < b.UID
	})
}

func threadSortDate(m MessageSummary) time.Time {
	if m.Date == nil {
		return time.Time{}
	}
	return *m.Date
}

// threadWindow picks the [start,end) slice for the requested window.
func threadWindow(members []MessageSummary, folder string, uid uint32, opts ThreadMembersOpts, total int) (int, int, error) {
	switch {
	case opts.Before != "":
		beforeFolder, beforeUID, err := parseThreadCursor(opts.Before)
		if err != nil {
			return 0, 0, ErrInvalidThreadCursor
		}
		p := indexOfThreadMember(members, beforeFolder, beforeUID)
		if p < 0 {
			return 0, 0, ErrInvalidThreadCursor
		}
		start := p - opts.Limit
		if start < 0 {
			start = 0
		}
		return start, p, nil
	case opts.AnchorUID != 0:
		p := indexOfThreadMember(members, folder, opts.AnchorUID)
		if p < 0 {
			return 0, 0, ErrThreadMemberNotFound
		}
		end := p + opts.Limit
		if end > total {
			end = total
		}
		start := end - opts.Limit
		if start < 0 {
			start = 0
		}
		// The anchor must sit inside the window; the newest-first fill above
		// guarantees it (window = anchor + up to Limit-1 newer members).
		return start, end, nil
	default:
		start := total - opts.Limit
		if start < 0 {
			start = 0
		}
		return start, total, nil
	}
}

func indexOfThreadMember(members []MessageSummary, folder string, uid uint32) int {
	for i := range members {
		if members[i].UID == uid && members[i].Folder == folder {
			return i
		}
	}
	return -1
}

func hasFlag(flags []string, want string) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}

// threadCursor renders the opaque `oldest`/`before` cursor "folder:uid".
// The folder name is path-escaped because IMAP names may contain "/".
func threadCursor(folder string, uid uint32) string {
	return url.PathEscape(folder) + ":" + strconv.FormatUint(uint64(uid), 10)
}

func parseThreadCursor(s string) (string, uint32, error) {
	i := strings.LastIndexByte(s, ':')
	if i <= 0 {
		return "", 0, ErrInvalidThreadCursor
	}
	folder, err := url.PathUnescape(s[:i])
	if err != nil {
		return "", 0, ErrInvalidThreadCursor
	}
	uid, err := strconv.ParseUint(s[i+1:], 10, 32)
	if err != nil {
		return "", 0, ErrInvalidThreadCursor
	}
	return folder, uint32(uid), nil
}

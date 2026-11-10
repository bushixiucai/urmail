package webmail

import (
	"context"
	"fmt"
	"sort"
)

// maxThreadRefineMessages bounds the additional headers fetched per list call.
// An oversized or incomplete group fails the request rather than returning
// server counts as though they had been checked.
const maxThreadRefineMessages = 2000

// refineSystemThreads checks complete server groups represented on this page.
// Every page containing a group fetches the same full group, so the split and
// its counts do not depend on which page contains a system sender.
func (c *imapConn) refineSystemThreads(ctx context.Context, page []MessageSummary, roots map[uint32]uint32, counts map[uint32]int) error {
	selected := make(map[uint32]bool)
	for _, m := range page {
		if root, ok := roots[m.UID]; ok && counts[root] > 1 {
			selected[root] = true
		}
	}
	if len(selected) == 0 {
		return nil
	}
	var uids []uint32
	for uid, root := range roots {
		if selected[root] {
			uids = append(uids, uid)
		}
	}
	if len(uids) > maxThreadRefineMessages {
		return fmt.Errorf("thread verification exceeds %d messages", maxThreadRefineMessages)
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	summaries, err := c.uidFetchSummaries(ctx, uids)
	if err != nil {
		return fmt.Errorf("thread verification: %w", err)
	}
	if len(summaries) != len(uids) {
		return fmt.Errorf("thread verification: incomplete summaries")
	}
	groups := make(map[uint32][]MessageSummary)
	for _, m := range summaries {
		groups[roots[m.UID]] = append(groups[roots[m.UID]], m)
	}
	for root, group := range groups {
		system := false
		for _, m := range group {
			for _, a := range m.From {
				if isSystemSender(a) {
					system = true
					break
				}
			}
			if system {
				break
			}
		}
		if !system {
			continue
		}
		refined := BuildThreads(group)
		delete(counts, root)
		for uid, newRoot := range refined {
			roots[uid] = newRoot
			counts[newRoot]++
		}
	}
	return nil
}

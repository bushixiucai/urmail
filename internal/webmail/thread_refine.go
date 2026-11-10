package webmail

import (
	"context"
	"errors"
	"sort"
)

// maxThreadRefineMessages bounds the number of summaries checked per request.
// Allocation is based on the complete server thread map, not the current page.
const maxThreadRefineMessages = 2000

// refineSystemThreads checks complete server groups represented on this page.
// Groups outside a stable global budget, or whose FETCH fails, are returned
// separately as unverified. A later successful request may verify them.
func (c *imapConn) refineSystemThreads(ctx context.Context, page []MessageSummary, roots map[uint32]uint32, counts map[uint32]int) (map[uint32]bool, error) {
	groupRoots := make([]uint32, 0, len(counts))
	for root, count := range counts {
		if count > 1 {
			groupRoots = append(groupRoots, root)
		}
	}
	sort.Slice(groupRoots, func(i, j int) bool { return groupRoots[i] < groupRoots[j] })
	eligible := make(map[uint32]bool)
	remaining := maxThreadRefineMessages
	for _, root := range groupRoots {
		if counts[root] <= remaining {
			eligible[root] = true
			remaining -= counts[root]
		}
	}
	selected := make(map[uint32]bool)
	unverified := make(map[uint32]bool)
	for _, m := range page {
		if root, ok := roots[m.UID]; ok && counts[root] > 1 {
			if eligible[root] {
				selected[root] = true
			} else {
				unverified[root] = true
			}
		}
	}
	if len(selected) == 0 {
		return unverified, nil
	}
	var uids []uint32
	for uid, root := range roots {
		if selected[root] {
			uids = append(uids, uid)
		}
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	summaries, err := c.uidFetchSummaries(ctx, uids)
	if err != nil || len(summaries) != len(uids) {
		var rejected imapCommandRejected
		if err != nil && !errors.As(err, &rejected) {
			// Keep a closed connection so later operations return an error.
			c.discard()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		for root := range selected {
			unverified[root] = true
		}
		return unverified, nil
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
	return unverified, nil
}

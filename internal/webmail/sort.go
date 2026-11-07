package webmail

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// SortKeys accepted by ListOpts.SortBy and the cross-folder search endpoint.
var SortKeys = map[string]bool{
	"date":    true,
	"subject": true,
	"from":    true,
	"size":    true,
}

// NormalizeSortKey validates a user-supplied sort key. Empty is allowed and
// means "arrival order" (UID descending).
func NormalizeSortKey(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", nil
	}
	if !SortKeys[s] {
		return "", fmt.Errorf("unsupported sort key %q", s)
	}
	return s, nil
}

// imapSortKey maps an API sort key to the IMAP SORT criterion.
func imapSortKey(key string, desc bool) string {
	var crit string
	switch key {
	case "subject":
		crit = "SUBJECT"
	case "from":
		crit = "FROM"
	case "size":
		crit = "SIZE"
	default:
		crit = "DATE"
	}
	if desc {
		return "REVERSE " + crit
	}
	return crit
}

// SortSummaries is the client-side fallback ordering used when the IMAP
// server does not advertise SORT. Tie-break by UID so the cursor contract
// stays stable: for equal keys the ascending direction puts the lower UID
// first, the descending direction the higher UID first.
// summaryDate treats an unparsable date as the zero time so sorting keeps a
// stable, non-panicking order for messages without a usable ENVELOPE date.
func summaryDate(m MessageSummary) time.Time {
	if m.Date == nil {
		return time.Time{}
	}
	return *m.Date
}

// SortSummaries orders message summaries in place by the requested key
// (date, subject, size, arrival) with UID tie-breaking.
func SortSummaries(msgs []MessageSummary, key string, desc bool) {
	if key == "" {
		// Arrival order: UID descending, newest first.
		sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].UID > msgs[j].UID })
		return
	}
	less := func(a, b MessageSummary) bool {
		switch key {
		case "subject":
			return strings.ToLower(a.Subject) < strings.ToLower(b.Subject)
		case "from":
			return strings.ToLower(firstFrom(a)) < strings.ToLower(firstFrom(b))
		case "size":
			return a.Size < b.Size
		default:
			return summaryDate(a).Before(summaryDate(b))
		}
	}
	sort.SliceStable(msgs, func(i, j int) bool {
		if less(msgs[i], msgs[j]) {
			return true
		}
		if less(msgs[j], msgs[i]) {
			return false
		}
		return msgs[i].UID < msgs[j].UID
	})
	if desc {
		reverseSummaries(msgs)
	}
}

func reverseSummaries(msgs []MessageSummary) {
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
}

func firstFrom(m MessageSummary) string {
	if len(m.From) == 0 {
		return ""
	}
	if m.From[0].Email != "" {
		return m.From[0].Email
	}
	return m.From[0].Name
}

// PageAfterCursor positions a page within an ordered UID sequence.
//
// It returns the UIDs for one page starting immediately after cursor (cursor
// 0 = sequence start). If cursor is absent from the sequence (expunged since
// the previous page), the page restarts from the sequence head; callers
// communicate that to the client via NextCursor, which resyncs on the next
// request. See the ListOpts contract comment.
func PageAfterCursor(ordered []uint32, cursor uint32, limit int) (page []uint32, next uint32) {
	if limit <= 0 {
		limit = DefaultPageSize
	}
	start := 0
	if cursor != 0 {
		found := false
		for i, uid := range ordered {
			if uid == cursor {
				start, found = i+1, true
				break
			}
		}
		if !found {
			// Cursor expunged: resync from the sequence head (documented in
			// the ListOpts contract).
			start = 0
		}
		if start >= len(ordered) {
			return nil, 0
		}
	}
	end := start + limit
	if end > len(ordered) {
		end = len(ordered)
	}
	page = ordered[start:end]
	if end < len(ordered) {
		next = ordered[end-1]
	}
	return page, next
}

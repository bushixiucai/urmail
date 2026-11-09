package webmail

import (
	"testing"
	"time"
)

func mk(uid uint32, subject, from string, size int64, date string) MessageSummary {
	t, err := time.Parse(time.RFC3339, date)
	if err != nil {
		panic(err)
	}
	return MessageSummary{UID: uid, Subject: subject, Size: size, Date: envelopeDate(t),
		From: []Address{{Email: from}}}
}

func TestNormalizeSortKey(t *testing.T) {
	for _, ok := range []string{"", "date", "SUBJECT", "From", " size "} {
		if _, err := NormalizeSortKey(ok); err != nil {
			t.Fatalf("key %q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"uid", "date;uid", "x)"} {
		if _, err := NormalizeSortKey(bad); err == nil {
			t.Fatalf("key %q accepted", bad)
		}
	}
}

func TestSortSummariesFallback(t *testing.T) {
	msgs := []MessageSummary{
		mk(3, "beta", "carol@x", 30, "2026-01-03T00:00:00Z"),
		mk(1, "alpha", "alice@x", 20, "2026-01-02T00:00:00Z"),
		mk(2, "alpha", "bob@x", 10, "2026-01-01T00:00:00Z"),
	}
	SortSummaries(msgs, "subject", false)
	if msgs[0].UID != 1 || msgs[1].UID != 2 || msgs[2].UID != 3 {
		t.Fatalf("subject asc tie-break wrong: %v", uids(msgs))
	}
	SortSummaries(msgs, "subject", true)
	if msgs[0].UID != 3 || msgs[1].UID != 2 || msgs[2].UID != 1 {
		t.Fatalf("subject desc tie-break wrong: %v", uids(msgs))
	}
	SortSummaries(msgs, "size", false)
	if msgs[0].UID != 2 || msgs[2].UID != 3 {
		t.Fatalf("size asc wrong: %v", uids(msgs))
	}
	SortSummaries(msgs, "from", false)
	if msgs[0].From[0].Email != "alice@x" {
		t.Fatalf("from asc wrong: %v", uids(msgs))
	}
	SortSummaries(msgs, "date", true)
	if msgs[0].UID != 3 {
		t.Fatalf("date desc wrong: %v", uids(msgs))
	}
	SortSummaries(msgs, "", false)
	if msgs[0].UID != 3 || msgs[2].UID != 1 {
		t.Fatalf("arrival order wrong: %v", uids(msgs))
	}
}

func uids(msgs []MessageSummary) []uint32 {
	out := make([]uint32, len(msgs))
	for i, m := range msgs {
		out[i] = m.UID
	}
	return out
}

func TestPageAfterCursor(t *testing.T) {
	ordered := []uint32{5, 6, 7, 8, 9}
	page, next := PageAfterCursor(ordered, 0, 2)
	if len(page) != 2 || page[0] != 5 || next != 6 {
		t.Fatalf("first page wrong: %v %d", page, next)
	}
	page, next = PageAfterCursor(ordered, 6, 2)
	if len(page) != 2 || page[0] != 7 || page[1] != 8 || next != 8 {
		t.Fatalf("cursor page wrong: %v %d", page, next)
	}
	page, _ = PageAfterCursor(ordered, 9, 2)
	if len(page) != 0 {
		t.Fatalf("exhausted page wrong: %v", page)
	}
	// Expunged cursor: resync from head.
	page, _ = PageAfterCursor(ordered, 99, 2)
	if page[0] != 5 {
		t.Fatalf("resync page wrong: %v", page)
	}
}

// New mail arriving between page requests must not duplicate or skip.
func TestPaginationStableUnderNewMail(t *testing.T) {
	// UID-descending arrival order snapshot used for page 1.
	snapshot := []uint32{9, 8, 7}
	page1, next1 := PageAfterCursor(snapshot, 0, 2)
	if len(page1) != 2 || page1[0] != 9 || page1[1] != 8 || next1 != 8 {
		t.Fatalf("page1 wrong: %v %d", page1, next1)
	}
	// New mail arrives (UID 10) and 5 expires before page 2.
	newer := []uint32{10, 9, 8, 7, 6}
	page2, next2 := PageAfterCursor(newer, next1, 2)
	if len(page2) != 2 || page2[0] != 7 || page2[1] != 6 || next2 != 0 {
		t.Fatalf("page2 wrong: %v %d", page2, next2)
	}
}

func TestBuildThreadsByReferences(t *testing.T) {
	// An orphan reply whose parent is unknown no longer merges by pure
	// same-sender overlap (Tiger decision v0.6.20 C1); a References pointer
	// to the root still merges it — References chains are strong grouping.
	msgs := []MessageSummary{
		{UID: 1, MessageID: "<a@x>", Subject: "Hi", From: []Address{{Email: "alice@x"}}},
		{UID: 2, InReplyTo: "<a@x>", Subject: "Re: Hi", From: []Address{{Email: "alice@x"}}},
		{UID: 3, MessageID: "<b@x>", Subject: "Other", From: []Address{{Email: "alice@x"}}},
		{UID: 4, InReplyTo: "<missing@x>", MessageID: "<c@x>", Subject: "Re: Hi", From: []Address{{Email: "alice@x"}}},
		{UID: 5, InReplyTo: "<missing@x>", References: []string{"<a@x>"}, MessageID: "<d@x>", Subject: "Re: Hi", From: []Address{{Email: "alice@x"}}},
	}
	threads := BuildThreads(msgs)
	for _, uid := range []uint32{1, 2, 5} {
		if threads[uid] != 1 {
			t.Fatalf("uid %d threaded to %d, want 1", uid, threads[uid])
		}
	}
	if threads[3] != 3 {
		t.Fatalf("uid 3 threaded to %d, want 3", threads[3])
	}
	if threads[4] != 4 {
		t.Fatalf("orphan reply uid 4 threaded to %d, want 4", threads[4])
	}
	ApplyThreads(msgs, threads)
	if msgs[1].ThreadID != 1 || msgs[1].ThreadCount != 3 {
		t.Fatalf("apply wrong: %+v", msgs[1])
	}
}

func TestBuildThreadsBySubjectFallback(t *testing.T) {
	// Same participants and one day apart: the subject fallback merges them.
	msgs := []MessageSummary{
		{UID: 7, Subject: "Re: Re: 计划", From: []Address{{Email: "alice@x"}}, Date: envelopeDate(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))},
		{UID: 5, Subject: "计划", From: []Address{{Email: "alice@x"}}, Date: envelopeDate(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))},
		{UID: 6, Subject: "Fwd: 计划", From: []Address{{Email: "alice@x"}}, Date: envelopeDate(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC))},
		{UID: 8, Subject: "unrelated", From: []Address{{Email: "alice@x"}}},
		{UID: 9, Subject: ""},
	}
	threads := BuildThreads(msgs)
	for _, uid := range []uint32{5, 6, 7} {
		if threads[uid] != 5 {
			t.Fatalf("uid %d threaded to %d, want 5", uid, threads[uid])
		}
	}
	if threads[8] != 8 || threads[9] != 9 {
		t.Fatalf("unrelated grouped: %v", threads)
	}
}

func TestParseThreadRoots(t *testing.T) {
	roots, counts, err := parseThreadRoots("(1 2 (3))(4)")
	if err != nil {
		t.Fatal(err)
	}
	want := map[uint32]uint32{1: 1, 2: 1, 3: 1, 4: 4}
	if len(roots) != len(want) {
		t.Fatalf("roots wrong: %v", roots)
	}
	for uid, root := range want {
		if roots[uid] != root {
			t.Fatalf("uid %d root %d want %d", uid, roots[uid], root)
		}
	}
	wantCounts := map[uint32]int{1: 3, 4: 1}
	for root, count := range wantCounts {
		if counts[root] != count {
			t.Fatalf("root %d count %d want %d", root, counts[root], count)
		}
	}
	if _, _, err := parseThreadRoots("(1"); err == nil {
		t.Fatal("unbalanced accepted")
	}
	if roots, counts, err := parseThreadRoots(""); err != nil || roots != nil || counts != nil {
		t.Fatalf("empty wrong: %v %v %v", roots, counts, err)
	}
}

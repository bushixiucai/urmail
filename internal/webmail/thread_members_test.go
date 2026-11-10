package webmail

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestThreadMembersMarksMissingAuxiliaryFolderBounded(t *testing.T) {
	folders := map[string][]memberMessage{
		"Sent": {{uid: 1, subject: "test", from: "a@example.test", messageID: "<a@x>"}},
	}
	client := dialTestClient(t, startMemberIMAPServer(t, folders))
	result, err := client.GetThreadMembers(context.Background(), "Sent", 1, ThreadMembersOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Scope != "bounded" || result.Total != 1 {
		t.Fatalf("result = %+v", result)
	}
}

// memberMessage is one message served by the multi-folder fake IMAP server.
type memberMessage struct {
	uid        uint32
	subject    string
	from       string
	date       string
	messageID  string
	inReplyTo  string
	references []string
	seen       bool
}

func (m memberMessage) envelope() string {
	inReplyTo := "NIL"
	if m.inReplyTo != "" {
		inReplyTo = fmt.Sprintf("%q", m.inReplyTo)
	}
	messageID := "NIL"
	if m.messageID != "" {
		messageID = fmt.Sprintf("%q", m.messageID)
	}
	mailbox, host := "alice", "example.test"
	if parts := strings.SplitN(m.from, "@", 2); len(parts) == 2 {
		mailbox, host = parts[0], parts[1]
	}
	return fmt.Sprintf("(%q %q ((NIL NIL %q %q)) NIL NIL NIL NIL NIL %s %s)",
		m.date, m.subject, mailbox, host, inReplyTo, messageID)
}

func (m memberMessage) fetchResponse(seq int) string {
	flags := ""
	if m.seen {
		flags = `\Seen`
	}
	refs := "NIL"
	if len(m.references) > 0 {
		refs = fmt.Sprintf("%q", "References: "+strings.Join(prefixAngle(m.references), " ")+"\r\n")
	}
	return fmt.Sprintf("* %d FETCH (UID %d FLAGS (%s) RFC822.SIZE 128 ENVELOPE %s BODYSTRUCTURE (\"TEXT\" \"PLAIN\" (\"CHARSET\" \"UTF-8\") NIL NIL \"7BIT\" 12 1) BODY[HEADER.FIELDS (REFERENCES)] %s)",
		seq, m.uid, flags, m.envelope(), refs)
}

func prefixAngle(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, "<"+id+">")
	}
	return out
}

// startMemberIMAPServer serves per-folder message sets: SELECT switches the
// folder, UID SEARCH and UID FETCH answer with that folder's set, and LIST
// advertises \Sent so the scan resolves it.
func startMemberIMAPServer(t *testing.T, folders map[string][]memberMessage) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		w := bufio.NewWriter(conn)
		r := bufio.NewReader(conn)
		current := "INBOX"
		_, _ = io.WriteString(w, "* OK ready\r\n")
		_ = w.Flush()
		for {
			line, readErr := r.ReadString('\n')
			if readErr != nil {
				return
			}
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			tag := fields[0]
			switch {
			case strings.Contains(line, "LIST"):
				resp := ""
				for name := range folders {
					flags := ""
					if name == "Sent" {
						flags = "\\Sent "
					}
					resp += fmt.Sprintf("* LIST (%s) \"/\" %s\r\n", flags, quoteIMAPName(name))
				}
				_, _ = io.WriteString(w, resp+tag+" OK list done\r\n")
			case strings.Contains(line, "SELECT"):
				current = unquoteIMAPName(lastField(line))
				if _, ok := folders[current]; !ok {
					_, _ = io.WriteString(w, tag+" NO no such folder\r\n")
					break
				}
				_, _ = io.WriteString(w, "* OK [UIDVALIDITY 7] selected\r\n"+tag+" OK select done\r\n")
			case strings.Contains(line, "UID THREAD"):
				_, _ = io.WriteString(w, tag+" NO UID THREAD unsupported\r\n")
			case strings.Contains(line, "UID SEARCH"):
				parts := make([]string, 0, len(folders[current]))
				for _, m := range folders[current] {
					parts = append(parts, strconv.FormatUint(uint64(m.uid), 10))
				}
				_, _ = io.WriteString(w, "* SEARCH "+strings.Join(parts, " ")+"\r\n"+tag+" OK search done\r\n")
			case strings.Contains(line, "UID FETCH"):
				out := ""
				for i, m := range folders[current] {
					out += m.fetchResponse(i+1) + "\r\n"
				}
				_, _ = io.WriteString(w, out+tag+" OK fetch done\r\n")
			case strings.Contains(line, "LOGOUT"):
				_, _ = io.WriteString(w, tag+" OK\r\n")
				_ = w.Flush()
				return
			default:
				_, _ = io.WriteString(w, tag+" OK\r\n")
			}
			_ = w.Flush()
		}
	}()
	return ln.Addr().String()
}

func lastField(line string) string {
	line = strings.TrimRight(line, "\r\n")
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

func quoteIMAPName(name string) string {
	if strings.ContainsAny(name, " ") {
		return `"` + name + `"`
	}
	return name
}

func unquoteIMAPName(s string) string {
	return strings.Trim(s, `"`)
}

func memberDate(day int, hour int) string {
	return time.Date(2026, 1, day, hour, 0, 0, 0, time.UTC).Format("02-Jan-2006 15:04:05 -0700")
}

// The strong References chain spans INBOX and Sent; the subject fallback
// must not fuse same-subject mail across folders.
func TestGetThreadMembersCrossFolderStrongChain(t *testing.T) {
	folders := map[string][]memberMessage{
		"INBOX": {
			{uid: 1, subject: "Plan", from: "alice@example.test", date: memberDate(1, 9), messageID: "<a@x>"},
			{uid: 2, subject: "Newsletter", from: "alice@example.test", date: memberDate(2, 9)},
			{uid: 3, subject: "Re: Plan", from: "bob@example.test", date: memberDate(3, 9), inReplyTo: "<a@x>"},
		},
		"Sent": {
			{uid: 10, subject: "Re: Plan", from: "bob@example.test", date: memberDate(3, 10), inReplyTo: "<a@x>"},
			{uid: 11, subject: "Re: Newsletter", from: "bob@example.test", date: memberDate(2, 10), references: []string{"<lost@x>"}},
		},
	}
	addr := startMemberIMAPServer(t, folders)
	client := dialTestClient(t, addr)
	defer func() { _ = client.Close() }()

	result, err := client.GetThreadMembers(context.Background(), "INBOX", 1, ThreadMembersOpts{})
	if err != nil {
		t.Fatalf("thread members: %v", err)
	}
	// Members: INBOX 1+3 via In-Reply-To, Sent 10 via In-Reply-To. Sent 11
	// must NOT join (subject fallback is folder-local; References point
	// outside the scan set).
	if result.Total != 3 {
		t.Fatalf("total = %d, want 3 (cross-folder chain only): %+v", result.Total, result)
	}
	if result.ThreadID != 1 {
		t.Fatalf("thread id = %d, want 1", result.ThreadID)
	}
	if len(result.Messages) != 3 {
		t.Fatalf("window = %d members, want 3", len(result.Messages))
	}
	// Oldest → newest: uid 1 (INBOX), uid 3 (INBOX), uid 10 (Sent).
	wantOrder := []struct {
		uid    uint32
		folder string
	}{{1, "INBOX"}, {3, "INBOX"}, {10, "Sent"}}
	for i, want := range wantOrder {
		got := result.Messages[i]
		if got.UID != want.uid || got.Folder != want.folder {
			t.Fatalf("member %d = (%d,%s), want (%d,%s)", i, got.UID, got.Folder, want.uid, want.folder)
		}
		if got.UIDValidity != 7 {
			t.Fatalf("member %d uid_validity = %d", i, got.UIDValidity)
		}
	}
	// The default window covers every member (3 ≤ limit), so neither bound
	// has more to fetch and the oldest cursor stays empty.
	if result.HasMoreOlder || result.HasMoreNewer || result.NewerCount != 0 {
		t.Fatalf("unexpected window bounds: %+v", result)
	}
	if result.HasMoreOlder && result.Oldest != "INBOX:1" {
		t.Fatalf("oldest cursor = %q", result.Oldest)
	}
}

// Anchor window: the anchor member plus up to limit-1 newer members,
// backfilled from the older side when fewer newer members exist.
func TestGetThreadMembersAnchorWindow(t *testing.T) {
	var inbox []memberMessage
	for i := 1; i <= 25; i++ {
		inbox = append(inbox, memberMessage{
			uid: uint32(i), subject: "Chain", from: "alice@example.test",
			date: memberDate(i, 9),
		})
	}
	// Chain them via Message-ID/In-Reply-To pairs so they group strongly.
	for i := range inbox {
		if i > 0 {
			inbox[i].inReplyTo = fmt.Sprintf("<m%d@x>", inbox[i-1].uid)
		}
		inbox[i].messageID = fmt.Sprintf("<m%d@x>", inbox[i].uid)
	}
	addr := startMemberIMAPServer(t, map[string][]memberMessage{"INBOX": inbox})
	client := dialTestClient(t, addr)
	defer func() { _ = client.Close() }()

	// Anchor on the old member uid 5 (e.g. arrived from list page 2).
	result, err := client.GetThreadMembers(context.Background(), "INBOX", 5, ThreadMembersOpts{AnchorUID: 5, Limit: 20})
	if err != nil {
		t.Fatalf("anchored window: %v", err)
	}
	if result.Total != 25 {
		t.Fatalf("total = %d, want 25", result.Total)
	}
	if len(result.Messages) != 20 {
		t.Fatalf("window = %d, want 20", len(result.Messages))
	}
	first := result.Messages[0]
	if first.UID != 5 || first.Folder != "INBOX" {
		t.Fatalf("window must start at the anchor (uid 5), got %d", first.UID)
	}
	// Anchor uid 5 with limit 20: window = uid 5..24; uids 1..4 stay older,
	// uid 25 stays newer.
	if !result.HasMoreOlder || result.Oldest != "INBOX:5" {
		t.Fatalf("older bound wrong: %+v", result)
	}
	if !result.HasMoreNewer || result.NewerCount != 1 {
		t.Fatalf("newer bound = %d/%v, want 1/true", result.NewerCount, result.HasMoreNewer)
	}

	// Anchor on a member with more than limit-1 newer members: the window
	// ends before the newest and newer_count reports the remainder.
	result, err = client.GetThreadMembers(context.Background(), "INBOX", 5, ThreadMembersOpts{AnchorUID: 5, Limit: 10})
	if err != nil {
		t.Fatalf("narrow anchored window: %v", err)
	}
	if len(result.Messages) != 10 || result.Messages[0].UID != 5 {
		t.Fatalf("narrow window wrong: first=%d len=%d", result.Messages[0].UID, len(result.Messages))
	}
	// uid 15..25 stay newer than the window.
	if !result.HasMoreNewer || result.NewerCount != 11 {
		t.Fatalf("newer count = %d/%v, want 11/true", result.NewerCount, result.HasMoreNewer)
	}
}

// `before` pagination walks strictly older pages until done; unread counts
// only the anchor folder.
func TestGetThreadMembersBeforePaginationAndUnread(t *testing.T) {
	var inbox []memberMessage
	for i := 1; i <= 23; i++ {
		msg := memberMessage{
			uid: uint32(i), subject: "Mega", from: "alice@example.test",
			date: memberDate(i, 9), seen: i%2 == 0,
		}
		if i > 1 {
			msg.inReplyTo = fmt.Sprintf("<m%d@x>", i-1)
		}
		msg.messageID = fmt.Sprintf("<m%d@x>", i)
		inbox = append(inbox, msg)
	}
	addr := startMemberIMAPServer(t, map[string][]memberMessage{"INBOX": inbox})
	client := dialTestClient(t, addr)
	defer func() { _ = client.Close() }()

	page1, err := client.GetThreadMembers(context.Background(), "INBOX", 23, ThreadMembersOpts{})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Messages) != 20 || !page1.HasMoreOlder {
		t.Fatalf("page1 = %d members, more=%v", len(page1.Messages), page1.HasMoreOlder)
	}
	// Thread-wide unread in the anchor folder (contract: badge counts the
	// whole thread, not just the loaded window): odd uids 1..23 = 12.
	if page1.UnreadCount != 12 {
		t.Fatalf("unread = %d, want 12", page1.UnreadCount)
	}
	if page1.Oldest != "INBOX:4" {
		t.Fatalf("oldest cursor = %q, want INBOX:4", page1.Oldest)
	}
	// Giant thread stamping rides along on the members.
	if !page1.Messages[19].WeakAggregation || page1.Messages[19].EarlierCount != 3 {
		t.Fatalf("weak aggregation missing on members: %+v", page1.Messages[19])
	}

	page2, err := client.GetThreadMembers(context.Background(), "INBOX", 23, ThreadMembersOpts{Before: page1.Oldest})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.Messages) != 3 || page2.HasMoreOlder {
		t.Fatalf("page2 = %d members, more=%v; want the 3 oldest and done", len(page2.Messages), page2.HasMoreOlder)
	}
	if page2.Messages[0].UID != 1 {
		t.Fatalf("page2 must start at uid 1, got %d", page2.Messages[0].UID)
	}

	// A garbage cursor is rejected, not silently treated as empty.
	if _, err := client.GetThreadMembers(context.Background(), "INBOX", 23, ThreadMembersOpts{Before: "bogus"}); err == nil {
		t.Fatalf("bogus cursor must error")
	}
}

// Subject fallback stays folder-local even when a Sent member shares the
// subject and a recipient with the INBOX group.
func TestGetThreadMembersFallbackStaysFolderLocal(t *testing.T) {
	folders := map[string][]memberMessage{
		"INBOX": {
			{uid: 1, subject: "Newsletter", from: "alice@example.test", date: memberDate(1, 9)},
		},
		"Sent": {
			// Bob's mail to Alice shares the subject and the recipient —
			// without a References chain it must not merge across folders.
			{uid: 10, subject: "Re: Newsletter", from: "bob@example.test", date: memberDate(2, 9)},
		},
	}
	addr := startMemberIMAPServer(t, folders)
	client := dialTestClient(t, addr)
	defer func() { _ = client.Close() }()

	result, err := client.GetThreadMembers(context.Background(), "INBOX", 1, ThreadMembersOpts{})
	if err != nil {
		t.Fatalf("thread members: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("cross-folder subject fallback fused members: %+v", result)
	}
}

// QA regression F-1 / fidelity DEF-4: UIDs collide across folders — an
// INBOX uid 1 and a Sent uid 1 are different messages. Threading on the
// raw UID fused unrelated threads (the isolated 五一 pair fused into the
// 42-message megathread); the synthetic-key grouping must keep them apart.
func TestGetThreadMembersUIDCollisionAcrossFolders(t *testing.T) {
	folders := map[string][]memberMessage{
		"INBOX": {
			{uid: 1, subject: "Megathread root", from: "alice@example.test", date: memberDate(1, 9), messageID: "<mega1@x>"},
			{uid: 2, subject: "Re: Megathread root", from: "bob@example.test", date: memberDate(1, 10), inReplyTo: "<mega1@x>"},
		},
		"Sent": {
			// Same uid number as the megathread root, zero header overlap.
			{uid: 1, subject: "五一出游计划", from: "me@example.test", date: memberDate(2, 9), messageID: "<s5-inbox@review-s5.example.test>"},
			{uid: 2, subject: "Re: 五一出游计划", from: "me@example.test", date: memberDate(2, 10), inReplyTo: "<s5-inbox@review-s5.example.test>"},
		},
	}
	addr := startMemberIMAPServer(t, folders)
	client := dialTestClient(t, addr)
	defer func() { _ = client.Close() }()

	result, err := client.GetThreadMembers(context.Background(), "Sent", 1, ThreadMembersOpts{})
	if err != nil {
		t.Fatalf("sent anchor: %v", err)
	}
	if result.Total != 2 {
		t.Fatalf("uid collision fused threads: total=%d, want 2: %+v", result.Total, result)
	}
	for _, m := range result.Messages {
		if m.Folder != "Sent" {
			t.Fatalf("cross-folder member leaked into isolated thread: %+v", m)
		}
	}
	// Anchoring from the INBOX side stays isolated too.
	resultInbox, err := client.GetThreadMembers(context.Background(), "INBOX", 1, ThreadMembersOpts{})
	if err != nil {
		t.Fatalf("inbox anchor: %v", err)
	}
	if resultInbox.Total != 2 {
		t.Fatalf("inbox anchor fused: total=%d, want 2", resultInbox.Total)
	}
}

// QA N2 verdict guard: a background-image url() without remote-image
// authorization is dropped server-side WITH the remote-images note.
func TestBackgroundImageDroppedWithNote(t *testing.T) {
	doc := `<div style="background-image:url(https://img.example.test/bg.png);color:#111">x</div>`
	inlined := InlineCSS(doc, UAClassDesktop, ColorSchemeNone)
	safe, notes := SanitizeHTMLOpts(inlined, SanitizeOptions{})
	if strings.Contains(strings.ToLower(safe), "background-image") {
		t.Fatalf("background-image survived without authorization: %s", safe)
	}
	joined := strings.Join(notes, ",")
	if !strings.Contains(joined, "remote") {
		t.Fatalf("remote-images note missing, notes=%v", notes)
	}
}

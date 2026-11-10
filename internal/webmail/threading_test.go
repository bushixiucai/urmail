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

// threadSummary builds a summary for the threading tests. The zero time
// yields a message without a date (Date == nil).
func threadSummary(uid uint32, subject, from string, date time.Time) MessageSummary {
	msg := MessageSummary{UID: uid, Subject: subject, From: []Address{{Email: from}}}
	if !date.IsZero() {
		msg.Date = envelopeDate(date)
	}
	return msg
}

func threadDate(day int) time.Time {
	return time.Date(2026, 1, day, 12, 0, 0, 0, time.UTC)
}

// Same subject but different participants: the fallback must not merge them.
// Without a usable date there is no time window either.
func TestThreadSubjectFallbackDifferentParticipantsStaySeparate(t *testing.T) {
	msgs := []MessageSummary{
		threadSummary(1, "Newsletter", "alice@example.test", time.Time{}),
		threadSummary(2, "Newsletter", "bob@example.test", time.Time{}),
	}
	threads := BuildThreads(msgs)
	if threads[1] != 1 || threads[2] != 2 {
		t.Fatalf("different participants merged: %v", threads)
	}
	ApplyThreads(msgs, threads)
	if msgs[0].ThreadCount != 1 || msgs[1].ThreadCount != 1 {
		t.Fatalf("counts wrong: %+v %+v", msgs[0], msgs[1])
	}
}

// Same subject, different participants, but far apart in time: still separate.
func TestThreadSubjectFallbackFarApartDatesStaySeparate(t *testing.T) {
	msgs := []MessageSummary{
		threadSummary(1, "Newsletter", "alice@example.test", threadDate(1)),
		threadSummary(2, "Newsletter", "bob@example.test", threadDate(20)),
	}
	threads := BuildThreads(msgs)
	if threads[1] != 1 || threads[2] != 2 {
		t.Fatalf("far apart messages merged: %v", threads)
	}
}

// Same subject and the same sole sender: pure same-sender overlap is not
// conversation evidence any more (Tiger decision v0.6.20 C1), so two
// broadcasts from one sender stay separate even under the same subject.
func TestThreadSubjectFallbackSameSenderStaysSeparate(t *testing.T) {
	msgs := []MessageSummary{
		threadSummary(1, "Newsletter", "alice@example.test", threadDate(1)),
		threadSummary(2, "Newsletter", "Alice@example.test", threadDate(25)),
	}
	threads := BuildThreads(msgs)
	if threads[2] != 2 {
		t.Fatalf("pure same-sender messages merged: %v", threads)
	}
	ApplyThreads(msgs, threads)
	if msgs[0].ThreadCount != 1 || msgs[1].ThreadCount != 1 {
		t.Fatalf("counts wrong: %+v %+v", msgs[0], msgs[1])
	}
}

// A shared recipient is conversation evidence: a reply whose References were
// lost still merges with the message it answers, even far apart in time.
func TestThreadSubjectFallbackSharedRecipientMerges(t *testing.T) {
	msgs := []MessageSummary{
		{UID: 1, Subject: "Quote", From: []Address{{Email: "alice@example.test"}}, Date: envelopeDate(threadDate(1))},
		{UID: 2, Subject: "Re: Quote", From: []Address{{Email: "bob@example.test"}}, To: []Address{{Email: "alice@example.test"}}, Date: envelopeDate(threadDate(25))},
	}
	threads := BuildThreads(msgs)
	if threads[2] != 1 {
		t.Fatalf("shared-recipient reply not merged: %v", threads)
	}
}

// System senders (bounce daemons, verifiers, auto-responders) never join the
// subject fallback: same subject, same system sender, within the window
// still stays separate (THREAD-UX-REVIEW F1 phantom threads).
func TestThreadSystemSendersStaySeparate(t *testing.T) {
	msgs := []MessageSummary{
		threadSummary(1, "Undelivered Mail Returned to Sender", "MAILER-DAEMON@mx.example.test", threadDate(1)),
		threadSummary(2, "Undelivered Mail Returned to Sender", "MAILER-DAEMON@mx.example.test", threadDate(2)),
		threadSummary(3, "Your verification code", "noreply@tutu.to", threadDate(1)),
		threadSummary(4, "Your verification code", "noreply@tutu.to", threadDate(2)),
	}
	threads := BuildThreads(msgs)
	for _, uid := range []uint32{1, 2, 3, 4} {
		if threads[uid] != uid {
			t.Fatalf("system mail uid %d threaded to %d, want itself", uid, threads[uid])
		}
	}
}

// A bounce that answers a real message still threads to it: the reply chain
// is strong grouping and is not touched by the system-sender exclusion.
func TestThreadSystemSenderChainStillGroups(t *testing.T) {
	msgs := []MessageSummary{
		{UID: 1, MessageID: "<a@x>", Subject: "Hi", From: []Address{{Email: "alice@example.test"}}, Date: envelopeDate(threadDate(1))},
		{UID: 2, MessageID: "<b@x>", InReplyTo: "<a@x>", Subject: "Undelivered Mail Returned to Sender", From: []Address{{Email: "MAILER-DAEMON@mx.example.test"}}, Date: envelopeDate(threadDate(1))},
	}
	threads := BuildThreads(msgs)
	if threads[2] != 1 || threads[1] != 1 {
		t.Fatalf("bounce chain split: %v", threads)
	}
}

// References chains group even when In-Reply-To is missing (some clients and
// bounce daemons fill References only).
func TestThreadReferencesChainGroups(t *testing.T) {
	msgs := []MessageSummary{
		{UID: 1, MessageID: "<a@x>", Subject: "Hi", From: []Address{{Email: "alice@example.test"}}, Date: envelopeDate(threadDate(1))},
		{UID: 2, MessageID: "<b@x>", References: []string{"<z@elsewhere>", "<a@x>"}, Subject: "Re: Hi", From: []Address{{Email: "bob@example.test"}}, Date: envelopeDate(threadDate(2))},
	}
	threads := BuildThreads(msgs)
	if threads[2] != 1 || threads[1] != 1 {
		t.Fatalf("references chain split: %v", threads)
	}
}

// Same subject, no participant overlap: the time window decides.
func TestThreadSubjectFallbackTimeWindow(t *testing.T) {
	within := []MessageSummary{
		threadSummary(1, "Report", "alice@example.test", threadDate(1)),
		threadSummary(2, "Report", "bob@example.test", threadDate(4)),
	}
	threads := BuildThreads(within)
	if threads[2] != 1 {
		t.Fatalf("messages within the window not merged: %v", threads)
	}
	// Exactly at the window edge: still merged.
	edge := []MessageSummary{
		threadSummary(1, "Report", "alice@example.test", threadDate(1)),
		threadSummary(2, "Report", "bob@example.test", threadDate(8)),
	}
	threads = BuildThreads(edge)
	if threads[2] != 1 {
		t.Fatalf("messages at the window edge not merged: %v", threads)
	}
	// Beyond the window: separate.
	beyond := []MessageSummary{
		threadSummary(1, "Report", "alice@example.test", threadDate(1)),
		threadSummary(2, "Report", "bob@example.test", threadDate(9)),
	}
	threads = BuildThreads(beyond)
	if threads[2] != 2 {
		t.Fatalf("messages beyond the window merged: %v", threads)
	}
	// A missing date never merges on subject alone.
	missing := []MessageSummary{
		threadSummary(1, "Report", "alice@example.test", threadDate(1)),
		threadSummary(2, "Report", "bob@example.test", time.Time{}),
	}
	threads = BuildThreads(missing)
	if threads[2] != 2 {
		t.Fatalf("message without a date merged: %v", threads)
	}
}

// A real reply chain stays one thread, whatever the fallback would decide,
// and the count covers the whole chain.
func TestThreadReplyChainSurvivesSubjectFallbackTightening(t *testing.T) {
	msgs := []MessageSummary{
		{UID: 1, MessageID: "<a@x>", Subject: "Hi", From: []Address{{Email: "alice@example.test"}}, Date: envelopeDate(threadDate(1))},
		{UID: 2, MessageID: "<b@x>", InReplyTo: "<a@x>", Subject: "Re: Hi", From: []Address{{Email: "bob@example.test"}}, Date: envelopeDate(threadDate(2))},
		{UID: 3, MessageID: "<c@x>", InReplyTo: "<b@x>", Subject: "Re: Hi", From: []Address{{Email: "carol@example.test"}}, Date: envelopeDate(threadDate(3))},
		// Same subject as the chain, unrelated sender, no date: the fallback
		// must not pull it into the chain.
		{UID: 4, MessageID: "<d@x>", Subject: "Re: Hi", From: []Address{{Email: "dave@example.test"}}},
	}
	threads := BuildThreads(msgs)
	for _, uid := range []uint32{1, 2, 3} {
		if threads[uid] != 1 {
			t.Fatalf("chain member %d threaded to %d, want 1", uid, threads[uid])
		}
	}
	if threads[4] != 4 {
		t.Fatalf("unrelated message threaded to %d, want 4", threads[4])
	}
	ApplyThreads(msgs, threads)
	for _, msg := range msgs[:3] {
		if msg.ThreadID != 1 || msg.ThreadCount != 3 {
			t.Fatalf("chain count wrong: %+v", msg)
		}
	}
	if msgs[3].ThreadID != 4 || msgs[3].ThreadCount != 1 {
		t.Fatalf("unrelated count wrong: %+v", msgs[3])
	}
}

// The count of a thread must not depend on the page that was loaded.
func TestThreadCountIsIndependentOfTheLoadedPage(t *testing.T) {
	full := []MessageSummary{
		{UID: 1, MessageID: "<a@x>", Subject: "Hi", From: []Address{{Email: "alice@example.test"}}},
		{UID: 2, MessageID: "<b@x>", InReplyTo: "<a@x>", Subject: "Re: Hi", From: []Address{{Email: "alice@example.test"}}},
		{UID: 3, MessageID: "<c@x>", InReplyTo: "<a@x>", Subject: "Re: Hi", From: []Address{{Email: "alice@example.test"}}},
	}
	threads := BuildThreads(full)
	for _, page := range [][]MessageSummary{{full[0]}, {full[1]}, {full[2]}} {
		ApplyThreads(page, threads)
		if page[0].ThreadID != 1 || page[0].ThreadCount != 3 {
			t.Fatalf("page count wrong: %+v", page[0])
		}
	}
}

// The server path counts every member of a thread, not only the loaded page.
func TestApplyThreadsWithCountsUsesServerThreadSizes(t *testing.T) {
	msgs := []MessageSummary{{UID: 7}, {UID: 8}}
	roots := map[uint32]uint32{7: 7, 8: 7}
	counts := map[uint32]int{7: 9}
	ApplyThreadsWithCounts(msgs, roots, counts)
	for _, msg := range msgs {
		if msg.ThreadID != 7 || msg.ThreadCount != 9 {
			t.Fatalf("server count wrong: %+v", msg)
		}
		if msg.WeakAggregation || msg.EarlierCount != 0 {
			t.Fatalf("weak aggregation stamped below threshold: %+v", msg)
		}
	}
}

// Giant threads stamp WeakAggregation and EarlierCount (Tiger decision
// v0.6.20 C1: newest-20 window plus an "earlier" summary bar). The count of
// exactly 20 stays a hard thread.
func TestApplyThreadsWithCountsWeakAggregation(t *testing.T) {
	msgs := []MessageSummary{{UID: 7}, {UID: 8}}
	roots := map[uint32]uint32{7: 7, 8: 8}
	counts := map[uint32]int{7: 761, 8: 20}
	ApplyThreadsWithCounts(msgs, roots, counts)
	if !msgs[0].WeakAggregation || msgs[0].EarlierCount != 741 {
		t.Fatalf("giant thread not weak-aggregated: %+v", msgs[0])
	}
	if msgs[1].WeakAggregation || msgs[1].EarlierCount != 0 {
		t.Fatalf("20-member thread must stay hard: %+v", msgs[1])
	}
}

// The IMAP THREAD response carries every member of a thread; the parsed
// counts must reflect the whole thread.
func TestParseThreadRootsMemberCounts(t *testing.T) {
	roots, counts, err := parseThreadRoots("(1 2 (3 4))(5)(6 7)")
	if err != nil {
		t.Fatal(err)
	}
	wantRoots := map[uint32]uint32{1: 1, 2: 1, 3: 1, 4: 1, 5: 5, 6: 6, 7: 6}
	for uid, root := range wantRoots {
		if roots[uid] != root {
			t.Fatalf("uid %d root %d want %d", uid, roots[uid], root)
		}
	}
	wantCounts := map[uint32]int{1: 4, 5: 1, 6: 2}
	for root, count := range wantCounts {
		if counts[root] != count {
			t.Fatalf("root %d count %d want %d", root, counts[root], count)
		}
	}
	if len(counts) != len(wantCounts) {
		t.Fatalf("counts wrong: %v", counts)
	}
}

// fakeThreadMessage describes one message served by the fake IMAP server.
type fakeThreadMessage struct {
	uid       uint32
	subject   string
	from      string
	date      string
	messageID string
	inReplyTo string
}

// envelope renders the message as an IMAP ENVELOPE s-expression.
func (m fakeThreadMessage) envelope() string {
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

// startThreadIMAPServer starts a fake IMAP server that answers UID SEARCH
// and UID FETCH for the given messages. When threadResponse is empty the
// server rejects UID THREAD so that the client threading path runs.
func startThreadIMAPServer(t *testing.T, msgs []fakeThreadMessage, threadResponse string) string {
	return startThreadIMAPServerObserved(t, msgs, threadResponse, nil)
}

func startThreadIMAPServerObserved(t *testing.T, msgs []fakeThreadMessage, threadResponse string, threadCalls chan<- struct{}) string {
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
			case strings.Contains(line, "UID THREAD"):
				if threadCalls != nil {
					threadCalls <- struct{}{}
				}
				if threadResponse == "" {
					_, _ = io.WriteString(w, tag+" NO UID THREAD unsupported\r\n")
				} else {
					_, _ = io.WriteString(w, "* THREAD "+threadResponse+"\r\n"+tag+" OK thread done\r\n")
				}
			case strings.Contains(line, "UID SEARCH"):
				parts := make([]string, 0, len(msgs))
				for _, m := range msgs {
					parts = append(parts, strconv.FormatUint(uint64(m.uid), 10))
				}
				_, _ = io.WriteString(w, "* SEARCH "+strings.Join(parts, " ")+"\r\n"+tag+" OK search done\r\n")
			case strings.Contains(line, "UID FETCH"):
				for _, m := range msgs {
					_, _ = io.WriteString(w, fmt.Sprintf(
						"* %d FETCH (UID %d FLAGS (\\Seen) RFC822.SIZE 128 ENVELOPE %s BODYSTRUCTURE (\"TEXT\" \"PLAIN\" (\"CHARSET\" \"UTF-8\") NIL NIL \"7BIT\" 12 1))\r\n",
						m.uid, m.uid, m.envelope()))
				}
				_, _ = io.WriteString(w, tag+" OK fetch done\r\n")
			case strings.Contains(line, "SELECT"):
				_, _ = io.WriteString(w, "* OK [UIDVALIDITY 7] selected\r\n"+tag+" OK select done\r\n")
			default:
				_, _ = io.WriteString(w, tag+" OK\r\n")
			}
			_ = w.Flush()
			if strings.Contains(line, "LOGOUT") {
				return
			}
		}
	}()
	return ln.Addr().String()
}

// threadFixtureMessages is one conversation of three messages (a reply chain)
// plus an unrelated message. The conversation holds the three newest UIDs, so
// the listing (newest first) walks through it page by page.
func threadFixtureMessages() []fakeThreadMessage {
	return []fakeThreadMessage{
		{uid: 1, subject: "Unrelated", from: "bob@example.test", date: "17-Oct-2026 09:00:00 +0000", messageID: "<d@x>"},
		{uid: 2, subject: "Re: Hi", from: "alice@example.test", date: "17-Oct-2026 11:00:00 +0000", messageID: "<b@x>", inReplyTo: "<a@x>"},
		{uid: 3, subject: "Re: Hi", from: "alice@example.test", date: "17-Oct-2026 12:00:00 +0000", messageID: "<c@x>", inReplyTo: "<a@x>"},
		{uid: 4, subject: "Hi", from: "alice@example.test", date: "17-Oct-2026 10:00:00 +0000", messageID: "<a@x>"},
	}
}

// The IMAP THREAD=REFERENCES path reports the whole thread size on every
// page, even when the members are spread over several pages.
func TestListMessagesServerThreadCountsCoverTheWholeThread(t *testing.T) {
	addr := startThreadIMAPServer(t, threadFixtureMessages(), "(4 3 2)(1)")
	client := dialTestClient(t, addr)

	first, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Limit: 1, Thread: true})
	if err != nil {
		t.Fatalf("list page 1: %v", err)
	}
	if first.ThreadMode != "server" || first.ThreadScope != ThreadScopeServer {
		t.Fatalf("thread mode wrong: %+v", first)
	}
	if len(first.Messages) != 1 {
		t.Fatalf("page 1 size %d want 1", len(first.Messages))
	}
	if first.Messages[0].ThreadCount != 3 {
		t.Fatalf("page 1 thread count %d want 3", first.Messages[0].ThreadCount)
	}

	second, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Limit: 1, Thread: true, CursorUID: first.NextCursor})
	if err != nil {
		t.Fatalf("list page 2: %v", err)
	}
	if len(second.Messages) != 1 {
		t.Fatalf("page 2 size %d want 1", len(second.Messages))
	}
	if second.Messages[0].ThreadCount != 3 {
		t.Fatalf("page 2 thread count %d want 3", second.Messages[0].ThreadCount)
	}
	if second.Messages[0].ThreadID != first.Messages[0].ThreadID {
		t.Fatalf("thread id differs per page: %d vs %d", second.Messages[0].ThreadID, first.Messages[0].ThreadID)
	}
}

// The client path (server without UID THREAD) counts the whole scanned set,
// so the count does not depend on the loaded page either.
func TestListMessagesClientThreadCountsCoverTheWholeThread(t *testing.T) {
	addr := startThreadIMAPServer(t, threadFixtureMessages(), "")
	client := dialTestClient(t, addr)

	first, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Limit: 1, Thread: true})
	if err != nil {
		t.Fatalf("list page 1: %v", err)
	}
	if first.ThreadMode != "client" || first.ThreadScope != ThreadScopeFolder {
		t.Fatalf("thread mode wrong: %+v", first)
	}
	if first.Messages[0].ThreadCount != 3 {
		t.Fatalf("page 1 thread count %d want 3", first.Messages[0].ThreadCount)
	}
	if first.Total != 4 {
		t.Fatalf("total %d want 4", first.Total)
	}

	second, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Limit: 1, Thread: true, CursorUID: first.NextCursor})
	if err != nil {
		t.Fatalf("list page 2: %v", err)
	}
	if second.Messages[0].ThreadCount != 3 {
		t.Fatalf("page 2 thread count %d want 3", second.Messages[0].ThreadCount)
	}
}

package webmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestPreviewFailurePreservesCommandSynchronization(t *testing.T) {
	for _, mode := range []string{"__preview_no__", "__preview_disconnect__"} {
		t.Run(mode, func(t *testing.T) {
			calls := make(chan struct{}, 1)
			client := dialTestClient(t, startThreadIMAPServerObserved(t, threadFixtureMessages(), mode, calls))
			_, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Limit: 1, Preview: true, Thread: true})
			if mode == "__preview_no__" && err != nil {
				t.Fatalf("tagged rejection: %v", err)
			}
			if mode == "__preview_disconnect__" && err == nil {
				t.Fatal("disconnect returned success")
			}
			select {
			case <-calls:
				if mode == "__preview_disconnect__" {
					t.Fatal("THREAD sent after interrupted preview")
				}
			default:
				if mode == "__preview_no__" {
					t.Fatal("tagged NO did not continue to THREAD")
				}
			}
		})
	}
}

func TestInterruptedCountAndRefinementCloseConnection(t *testing.T) {
	for _, mode := range []string{"__count_stall__", "__refine_disconnect__", "__select_stall__", "__initial_search_stall__", "__page_stall__"} {
		t.Run(mode, func(t *testing.T) {
			client := dialTestClient(t, startThreadIMAPServer(t, threadFixtureMessages(), mode))
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			result, err := client.ListMessages(ctx, "INBOX", ListOpts{Limit: 1, Thread: true})
			if mode != "__refine_disconnect__" && err == nil {
				t.Fatal("canceled count returned success")
			}
			if mode == "__refine_disconnect__" && (err != nil || result.Messages[0].ThreadVerification != "unverified") {
				t.Fatalf("unexpected refinement result: %+v, %v", result, err)
			}
			if _, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Limit: 1}); err == nil {
				t.Fatal("closed connection accepted another operation")
			}
		})
	}
}

func TestThreadTransportFailureClosesConnection(t *testing.T) {
	for _, mode := range []string{"__disconnect__", "__stall__"} {
		t.Run(mode, func(t *testing.T) {
			client := dialTestClient(t, startThreadIMAPServer(t, threadFixtureMessages(), mode))
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, err := client.ListMessages(ctx, "INBOX", ListOpts{Limit: 1, Thread: true})
			if err == nil {
				t.Fatal("expected transport or cancellation error")
			}
			var netErr net.Error
			if mode == "__stall__" && !errors.Is(err, context.DeadlineExceeded) && (!errors.As(err, &netErr) || !netErr.Timeout()) {
				t.Fatalf("expected deadline or network timeout, got %v", err)
			}
			if !client.(*imapConn).closed {
				t.Fatal("interrupted connection was not closed")
			}
			if _, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Limit: 1}); err == nil {
				t.Fatal("closed connection accepted another operation")
			}
		})
	}
}

func TestThreadExplicitCancellationClosesConnection(t *testing.T) {
	calls := make(chan struct{}, 1)
	client := dialTestClient(t, startThreadIMAPServerObserved(t, threadFixtureMessages(), "__stall__", calls))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := client.ListMessages(ctx, "INBOX", ListOpts{Limit: 1, Thread: true})
		result <- err
	}()
	<-calls
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if !client.(*imapConn).closed {
		t.Fatal("canceled connection was not closed")
	}
	if _, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Limit: 1}); err == nil {
		t.Fatal("closed connection accepted another operation")
	}
}

func TestServerThreadFolderCountBoundary(t *testing.T) {
	for _, count := range []int{300, 301} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			msgs := make([]fakeThreadMessage, count)
			for i := range msgs {
				msgs[i] = fakeThreadMessage{uid: uint32(i + 1), subject: "test", from: "a@example.test"}
			}
			calls := make(chan struct{}, 1)
			client := dialTestClient(t, startThreadIMAPServerObserved(t, msgs, "(1)", calls))
			result, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Limit: 1, Thread: true})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-calls:
				if count > 300 {
					t.Fatal("large folder sent THREAD")
				}
			default:
				if count <= 300 {
					t.Fatal("small folder did not send THREAD")
				}
			}
			if count > 300 && (result.ThreadScope != "unverified" || result.Messages[0].ThreadVerification != "unverified" || result.Messages[0].ThreadID != 0 || result.Messages[0].ThreadCount != 0) {
				t.Fatalf("unexpected large-folder result: %+v", result)
			}
		})
	}
}

func TestRefineBudgetIndependentOfPageOrder(t *testing.T) {
	roots := make(map[uint32]uint32)
	counts := map[uint32]int{1: 1001, 1002: 1000}
	for uid := uint32(1); uid <= 2001; uid++ {
		root := uint32(1)
		if uid >= 1002 {
			root = 1002
		}
		roots[uid] = root
	}
	// The second group cannot fit the global budget even when requested first.
	for _, uid := range []uint32{1002, 2001, 1500} {
		unverified, err := (*imapConn)(nil).refineSystemThreads(context.Background(), []MessageSummary{{UID: uid}}, roots, counts)
		if err != nil || !unverified[1002] {
			t.Fatalf("uid %d: %v %v", uid, unverified, err)
		}
	}
}

func TestListUnverifiedLargeGroupKeepsEveryRow(t *testing.T) {
	msgs := make([]fakeThreadMessage, maxThreadRefineMessages+1)
	ids := make([]string, 0, len(msgs))
	for i := range msgs {
		uid := uint32(i + 1)
		msgs[i] = fakeThreadMessage{uid: uid, subject: "same", from: "a@example.test", messageID: fmt.Sprintf("<%d@x>", uid)}
		ids = append(ids, fmt.Sprint(uid))
	}
	threadCalls := make(chan struct{}, 4)
	client := dialTestClient(t, startThreadIMAPServerObserved(t, msgs, "("+strings.Join(ids, " ")+")", threadCalls))
	var cursor uint32
	seen := make(map[uint32]bool)
	for i := 0; i < 3; i++ {
		result, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Limit: 2, Thread: true, CursorUID: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Messages) != 2 || result.ThreadScope != "unverified" {
			t.Fatalf("page: %+v", result)
		}
		for _, m := range result.Messages {
			if seen[m.UID] || m.ThreadID != 0 || m.ThreadCount != 0 || m.ThreadVerification != "unverified" {
				t.Fatalf("bad row: %+v", m)
			}
			seen[m.UID] = true
			encoded, err := json.Marshal(m)
			if err != nil || strings.Contains(string(encoded), `"thread_id"`) || strings.Contains(string(encoded), `"thread_count"`) {
				t.Fatalf("bad wire row %s: %v", encoded, err)
			}
		}
		cursor = result.NextCursor
	}
	select {
	case <-threadCalls:
		t.Fatal("large folder sent UID THREAD")
	default:
	}
}

func TestListRefinesSystemThreadsAcrossPages(t *testing.T) {
	msgs := []fakeThreadMessage{
		{uid: 1, subject: "Returned", from: "MAILER-DAEMON@example.test", messageID: "<a@x>"},
		{uid: 2, subject: "Returned", from: "postmaster@example.test", messageID: "<b@x>"},
		{uid: 3, subject: "Returned", from: "MAILER-DAEMON@box.example.test", messageID: "<c@x>"},
	}
	client := dialTestClient(t, startThreadIMAPServer(t, msgs, "(1 2 3)"))
	var cursor uint32
	for i := 0; i < 3; i++ {
		result, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Limit: 1, Thread: true, CursorUID: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Messages) != 1 || result.Messages[0].ThreadCount != 1 || result.Messages[0].ThreadID != result.Messages[0].UID {
			t.Fatalf("page %d: %+v", i, result)
		}
		cursor = result.NextCursor
	}
}

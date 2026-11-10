package webmail

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

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
	var ids []string
	for i := range msgs {
		uid := uint32(i + 1)
		msgs[i] = fakeThreadMessage{uid: uid, subject: "same", from: "a@example.test", messageID: fmt.Sprintf("<%d@x>", uid)}
		ids = append(ids, fmt.Sprint(uid))
	}
	client := dialTestClient(t, startThreadIMAPServer(t, msgs, "("+strings.Join(ids, " ")+")"))
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

package webmail

import (
	"context"
	"testing"
)

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

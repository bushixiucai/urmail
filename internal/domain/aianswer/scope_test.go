package aianswer

import (
	"errors"
	"testing"
)

// TestSourcesRejectAnotherMailbox implements the test sources reject another mailbox step.
func TestSourcesRejectAnotherMailbox(t *testing.T) {
	_, err := SourcesForMailbox(7, []ScopedSource{{MailboxID: 8, Source: Source{ID: "m1", Quote: "hello"}}})
	if !errors.Is(err, ErrUngrounded) {
		t.Fatal(err)
	}
}

// TestSourcesKeepCurrentMailbox implements the test sources keep current mailbox step.
func TestSourcesKeepCurrentMailbox(t *testing.T) {
	got, err := SourcesForMailbox(7, []ScopedSource{{MailboxID: 7, Source: Source{ID: "m1", Quote: "hello"}}})
	if err != nil || len(got) != 1 || got[0].ID != "m1" {
		t.Fatalf("%+v %v", got, err)
	}
}

package aianswer

import (
	"context"
	"errors"
	"testing"
)

// TestAskDoesNotCallModelWithoutSources implements the test ask does not call model without sources step.
func TestAskDoesNotCallModelWithoutSources(t *testing.T) {
	called := false
	model := func(context.Context, string, []Source) (string, []string, error) {
		called = true
		return "invented", []string{"x"}, nil
	}
	got, err := Ask(context.Background(), model, "Where is the invoice?", nil)
	if err != nil || called || got.Grounded || len(got.Citations) != 0 {
		t.Fatalf("%+v %v %v", got, called, err)
	}
}

// TestAskMailboxRejectsForeignSourceBeforeModel implements the test ask mailbox rejects foreign source before model step.
func TestAskMailboxRejectsForeignSourceBeforeModel(t *testing.T) {
	called := false
	model := func(context.Context, string, []Source) (string, []string, error) { called = true; return "x", nil, nil }
	_, err := AskMailbox(context.Background(), model, 7, "Where is it?", []ScopedSource{{MailboxID: 8, Source: Source{ID: "m", Quote: "q"}}})
	if !errors.Is(err, ErrUngrounded) || called {
		t.Fatal(err, called)
	}
}

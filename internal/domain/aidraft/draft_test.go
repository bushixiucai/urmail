package aidraft

import (
	"errors"
	"testing"
)

func TestParseRejectsMultilineSubject(t *testing.T) {
	if _, err := Parse("Hello\nSend", "body", 1); !errors.Is(err, ErrInvalidDraft) {
		t.Fatal(err)
	}
}

func TestLateGenerationDoesNotOverwriteUserEdit(t *testing.T) {
	current, _ := Parse("Mine", "kept", 3)
	proposed, _ := Parse("Old", "generated", 2)
	got, err := Edit(current, proposed)
	if err != nil || got.Body != "kept" || got.Version != 3 {
		t.Fatalf("%+v %v", got, err)
	}
}

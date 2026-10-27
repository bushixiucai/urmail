package mail

import "testing"

func TestReplyPrefersReplyToAndDropsSelf(t *testing.T) {
	got := Reply(Parties{
		From:    []string{"author@example.test"},
		ReplyTo: []string{"list@example.test"},
		To:      []string{"me@example.test"},
		Self:    []string{"me@example.test"},
	})
	if len(got) != 1 || got[0] != "list@example.test" {
		t.Fatalf("reply=%v", got)
	}
}

func TestReplyAllKeepsVisibleParticipantsOnly(t *testing.T) {
	to, cc := ReplyAll(Parties{
		From: []string{"author@example.test"},
		To:   []string{"me@example.test", "bob@example.test", "bob@example.test"},
		Cc:   []string{"cara@example.test", "author@example.test"},
		Self: []string{"Me@Example.Test"},
	})
	if join(to) != "author@example.test" || join(cc) != "bob@example.test,cara@example.test" {
		t.Fatalf("to=%v cc=%v", to, cc)
	}
}

func TestReplyAllUsesReplyToAsTheOnlyAuthor(t *testing.T) {
	to, cc := ReplyAll(Parties{
		From:    []string{"hidden-author@example.test"},
		ReplyTo: []string{"desk@example.test"},
		To:      []string{"me@example.test"},
		Cc:      []string{"cara@example.test"},
		Self:    []string{"me@example.test"},
	})
	if join(to) != "desk@example.test" || join(cc) != "cara@example.test" {
		t.Fatalf("to=%v cc=%v", to, cc)
	}
	for _, addr := range append(to, cc...) {
		if addr == "hidden-author@example.test" {
			t.Fatal("from leaked beside reply-to")
		}
	}
}

func join(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

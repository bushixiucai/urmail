package recall

import (
	"strings"
	"testing"
)

func TestSnapshotBindsGenerationActorHoldsAndExactSelection(t *testing.T) {
	s := Snapshot{Version: 1, TenantID: 1, ActorID: 2, EntryID: 3, MailboxID: 4, Address: "alice@example.test", MessageID: "<exact@example.test>", OriginalSHA256: strings.Repeat("a", 64), TenantStatus: "active", MailboxStatus: "active", DomainStatus: "active", Copies: []Copy{{GUID: "0123456789abcdef0123456789abcdef", MessageGUID: "message-file-1", Folder: "INBOX", UIDValidity: 8, UID: 2}, {GUID: "0123456789abcdef0123456789abcdef", MessageGUID: "message-file-1", Folder: "INBOX", UIDValidity: 8, UID: 1}}}
	a, err := s.Digest()
	if err != nil {
		t.Fatal(err)
	}
	s.Copies[0], s.Copies[1] = s.Copies[1], s.Copies[0]
	b, err := s.Digest()
	if err != nil || a != b {
		t.Fatal("ordering changed proof")
	}
	selected, err := s.Select([]string{s.Copies[0].Key()})
	if err != nil || len(selected) != 1 || selected[0].UID != 1 {
		t.Fatal("explicit copy selection failed")
	}
	for _, keys := range [][]string{{}, {"wildcard"}, {s.Copies[0].Key(), s.Copies[0].Key()}} {
		if _, err = s.Select(keys); err == nil {
			t.Fatal("ambiguous selection accepted")
		}
	}
	s.Copies[0].UIDValidity++
	changed, _ := s.Digest()
	if changed == a {
		t.Fatal("generation change retained proof")
	}
	s.HoldConflict = true
	if s.Refusal() != "legal_hold_conflict" {
		t.Fatal("held sibling was selectable")
	}
}

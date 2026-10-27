package webmail

import "testing"

func TestResolveSentAndTrashPreferSpecialUse(t *testing.T) {
	folders := []Folder{
		{Name: "Sent Items", Flags: []string{`\Sent`}},
		{Name: "Trash", Flags: []string{`\Trash`}},
		{Name: "Sent"},
	}
	if got := ResolveSentFolder(folders); got != "Sent Items" {
		t.Fatalf("sent=%s", got)
	}
	if got := ResolveTrashFolder(folders); got != "Trash" {
		t.Fatalf("trash=%s", got)
	}
	if got := ResolveSentFolder([]Folder{{Name: "sent"}}); got != "sent" {
		t.Fatalf("fallback=%s", got)
	}
}

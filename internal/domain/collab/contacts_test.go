package collab

import (
	"errors"
	"testing"
)

func TestNormalizeContactDedupesAndRejectsInjection(t *testing.T) {
	got, err := NormalizeContact(Contact{
		DisplayName: "  Ada Lovelace ",
		Emails: []Email{
			{Address: " Ada@Example.Test "},
			{Address: "ada@example.test"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "Ada Lovelace" || len(got.Emails) != 1 || got.Emails[0].Address != "ada@example.test" {
		t.Fatalf("%+v", got)
	}
	if _, err := NormalizeContact(Contact{Emails: []Email{{Address: "not-an-email"}}}); !errors.Is(err, ErrInvalidContact) {
		t.Fatalf("err=%v", err)
	}
	if _, err := NormalizeContact(Contact{}); !errors.Is(err, ErrInvalidContact) {
		t.Fatalf("empty err=%v", err)
	}
}

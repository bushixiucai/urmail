package collab

import (
	"errors"
	"strings"
	"testing"
)

func TestParseContactsCSV(t *testing.T) {
	got, err := ParseContactsCSV("name,email\nAda,ada@example.test\n")
	if err != nil || len(got) != 1 || got[0].Emails[0].Address != "ada@example.test" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestParseContactsCSVRejectsTooManyRows(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 1001; i++ {
		b.WriteString("A,a@example.test\n")
	}
	if _, err := ParseContactsCSV(b.String()); !errors.Is(err, ErrInvalidContact) {
		t.Fatal(err)
	}
}

package collab

import (
	"errors"
	"strings"
	"testing"
)

func TestVCardRoundTrip(t *testing.T) {
	raw, err := VCard(Contact{DisplayName: "Ada; Lovelace", Emails: []Email{{Address: "ada@example.test"}}, Note: "line\n2"})
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(raw, "FN:Ada\\; Lovelace") {
		t.Fatalf("raw=%s", raw)
	}
	got, err := ParseVCard(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "Ada; Lovelace" || got.Emails[0].Address != "ada@example.test" || got.Note != "line\n2" {
		t.Fatalf("%+v", got)
	}
}

func TestParseVCardRejectsTwoCards(t *testing.T) {
	raw := "BEGIN:VCARD\nFN:A\nEMAIL:a@example.test\nEND:VCARD\nBEGIN:VCARD\nFN:B\nEND:VCARD\n"
	if _, err := ParseVCard(raw); !errors.Is(err, ErrInvalidContact) {
		t.Fatalf("err=%v", err)
	}
}

func containsText(s, part string) bool {
	return strings.Contains(s, part)
}

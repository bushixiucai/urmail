package eas

import (
	"reflect"
	"testing"

	"github.com/bushixiucai/urmail/internal/domain/collab"
)

func TestContactClientWritePreservesUnrepresentedFields(t *testing.T) {
	base := collab.Contact{DisplayName: "Ada", GivenName: "Ada", FamilyName: "Lovelace", Emails: []collab.Email{{Address: "one@example.test", Label: "work"}, {Address: "two@example.test"}, {Address: "three@example.test"}, {Address: "extra@example.test"}}, Phones: []collab.Phone{{Label: "mobile", Number: "123"}, {Label: "custom", Number: "456"}}, Note: "original note"}
	got, err := parseContactWrite(Element(pageAirSync, tokApplicationData, Text(pageContacts, tokContactFirstName, "Augusta")), base)
	if err != nil || got.GivenName != "Augusta" || !reflect.DeepEqual(got.Emails, base.Emails) || !reflect.DeepEqual(got.Phones, base.Phones) || got.Note != base.Note {
		t.Fatalf("partial update lost fields: %+v %v", got, err)
	}
	named, err := parseContactWrite(Element(pageAirSync, tokApplicationData, Text(pageContacts, tokContactEmail1Address, `"Ada Lovelace" <ADA@example.test>`)), base)
	if err != nil || named.Emails[0].Address != "ada@example.test" {
		t.Fatalf("RFC mailbox input rejected %+v %v", named, err)
	}
	for _, bad := range []*Node{Text(pageContacts, 0x19, "Cannot silently drop company"), Element(pageContacts, 0x15, Text(pageContacts, 0x16, "category")), Element(pageAirSyncBase, tokBaseBody, Text(pageAirSyncBase, tokBaseType, "2"), Text(pageAirSyncBase, tokBaseData, "html")), Element(pageAirSyncBase, tokBaseBody, Text(pageAirSyncBase, tokBaseTruncated, "1"))} {
		if _, err := parseContactWrite(Element(pageAirSync, tokApplicationData, bad), base); err == nil {
			t.Fatalf("unsupported data accepted: %+v", bad)
		}
	}
	got, err = parseContactWrite(Element(pageAirSync, tokApplicationData, Text(pageContacts, tokContactEmail2Address, ""), Text(pageContacts, tokContactMobilePhoneNumber, ""), Element(pageAirSyncBase, tokBaseBody, Text(pageAirSyncBase, tokBaseType, "1"), Text(pageAirSyncBase, tokBaseData, "new note"))), base)
	if err != nil || len(got.Emails) != 3 || len(got.Phones) != 1 || got.Note != "new note" {
		t.Fatalf("explicit clearing failed: %+v %v", got, err)
	}
}

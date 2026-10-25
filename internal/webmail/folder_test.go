package webmail

import "testing"

func TestEncodeDecodeFolderName(t *testing.T) {
	cases := []string{"INBOX", "INBOX.Sent", "Sent Messages", "Archive/2024"}
	for _, name := range cases {
		enc := EncodeFolderName(name)
		got, err := DecodeFolderName(enc)
		if err != nil || got != name {
			t.Fatalf("roundtrip %q via %q: got %q err %v", name, enc, got, err)
		}
	}
	got, err := DecodeFolderName("INBOX")
	if err != nil || got != "INBOX" {
		t.Fatalf("plain INBOX: %q %v", got, err)
	}
	if _, err := DecodeFolderName("b64.!!!"); err == nil {
		t.Fatal("expected bad b64")
	}
	if _, err := DecodeFolderName(""); err == nil {
		t.Fatal("expected empty reject")
	}
}

func TestClampListOpts(t *testing.T) {
	got := ClampListOpts(ListOpts{Limit: 0, Offset: -1})
	if got.Limit != DefaultPageSize || got.Offset != 0 {
		t.Fatalf("defaults = %+v", got)
	}
	got = ClampListOpts(ListOpts{Limit: 999})
	if got.Limit != MaxPageSize {
		t.Fatalf("cap = %d", got.Limit)
	}
}

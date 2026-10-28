package webmail

import "testing"

func TestAdvancedSearchGrammar(t *testing.T) {
	criteria, attachment, err := buildSearchPlan(`since:2024-02-29 before:2026-10-28 to:"Bob Smith" body:invoice has:attachment`)
	if err != nil || !attachment || criteria != `SINCE 29-Feb-2024 BEFORE 28-Oct-2026 TO "Bob Smith" BODY "invoice"` {
		t.Fatalf("%q %v %v", criteria, attachment, err)
	}
	for _, query := range []string{`since:2025-02-29`, `since:0000-01-01`, `before:2026-1-01`, `has:attachments`, `to:`, `body:`, `before:"2026-10-28 OR ALL"`} {
		if _, _, err := buildSearchPlan(query); err == nil {
			t.Fatalf("accepted %q", query)
		}
	}
}
func TestMIMEAttachmentMetadata(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{`("TEXT" "PLAIN" ("CHARSET" "utf-8") NIL NIL "7BIT" 12 1 NIL NIL NIL)`, false},
		{`("APPLICATION" "PDF" NIL NIL NIL "BASE64" 120 NIL ("ATTACHMENT" ("FILENAME" "invoice.pdf")) NIL)`, true},
		{`("IMAGE" "PNG" ("NAME" "logo.png") NIL NIL "BASE64" 20 NIL ("INLINE" NIL) NIL)`, true},
		{`("TEXT" "PLAIN" NIL NIL NIL "7BIT" 12 1 NIL ("INLINE" ("FILENAME" "notes.txt")) NIL)`, true},
		{`(("TEXT" "PLAIN" NIL NIL NIL "7BIT" 12 1 NIL NIL NIL)("APPLICATION" "PDF" NIL NIL NIL "BASE64" 120 NIL ("ATTACHMENT" NIL) NIL) "MIXED" ("BOUNDARY" "test") NIL NIL)`, true},
		{`(("TEXT" "PLAIN" NIL NIL NIL "7BIT" 12 1 NIL NIL NIL) "ALTERNATIVE" ("BOUNDARY" "attachment") NIL NIL)`, false},
	}
	for _, tc := range cases {
		if got := bodyStructureHasAttachment(tc.body, 0); got != tc.want {
			t.Fatalf("got %v for %s", got, tc.body)
		}
	}
}

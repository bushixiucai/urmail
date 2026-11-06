package webmail

import (
	"errors"
	"strings"
	"testing"
)

func TestBuildSearchCriteria(t *testing.T) {
	cases := []struct {
		q    string
		want string
	}{
		{"", "ALL"},
		{"   ", "ALL"},
		{"unseen", "UNSEEN"},
		{"is:unseen", "UNSEEN"},
		{"IS:UNSEEN", "UNSEEN"},
		{"flagged", "FLAGGED"},
		{"is:flagged", "FLAGGED"},
		{"IS:FLAGGED", "FLAGGED"},
		{"is:flagged is:unseen", "FLAGGED UNSEEN"},
		{`from:alice@example.test`, `FROM "alice@example.test"`},
		{`subject:Invoice`, `SUBJECT "Invoice"`},
		{`text:refund`, `TEXT "refund"`},
		{`hello`, `OR SUBJECT "hello" FROM "hello"`},
		{`hello world`, `OR SUBJECT "hello world" FROM "hello world"`},
		{`from:bob is:unseen`, `FROM "bob" UNSEEN`},
		{`subject:"foo bar" from:z`, `SUBJECT "foo bar" FROM "z"`},
		{`invoice is:unseen`, `UNSEEN OR SUBJECT "invoice" FROM "invoice"`},
		{`from:"Alice \"A\""`, `FROM "Alice \"A\""`},
	}
	for _, tc := range cases {
		got, err := BuildSearchCriteria(tc.q)
		if err != nil {
			t.Fatalf("q=%q err=%v", tc.q, err)
		}
		if got != tc.want {
			t.Fatalf("q=%q\n got %q\nwant %q", tc.q, got, tc.want)
		}
	}
}

func TestBuildSearchCriteriaRejects(t *testing.T) {
	bads := []string{
		"before:1",
		"or:x",
		"from:",
		"subject:",
		"text:",
		"\"unclosed",
		"a\nb",
		"a\x00b",
		strings.Repeat("x", MaxSearchQueryLen+1),
	}
	for _, q := range bads {
		_, err := BuildSearchCriteria(q)
		if !errors.Is(err, ErrInvalidSearchQuery) {
			t.Fatalf("q=%q err=%v, want ErrInvalidSearchQuery", q, err)
		}
	}
}

func TestBuildSearchCriteriaNoInjection(t *testing.T) {
	// Bare quote mid-token is rejected (unclosed / malformed), not passed through.
	if _, err := BuildSearchCriteria(`subject:a"b`); !errors.Is(err, ErrInvalidSearchQuery) {
		t.Fatalf("expected reject, got %v", err)
	}
	got, err := BuildSearchCriteria(`subject:"a\"b"`)
	if err != nil {
		t.Fatal(err)
	}
	if got != `SUBJECT "a\"b"` {
		t.Fatalf("not escaped: %q", got)
	}
	if strings.Contains(got, "\r") || strings.Contains(got, "\n") {
		t.Fatalf("newline in criteria: %q", got)
	}
}

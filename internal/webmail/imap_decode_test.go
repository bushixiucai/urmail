package webmail

import (
	"encoding/json"
	"testing"
	"time"
)

// Real-world CJK encoded-words must decode instead of falling back to the raw
// ASCII armored form (production screenshots showed mojibake subjects).
func TestDecodeMIMEHeaderSimplifiedChinese(t *testing.T) {
	got := decodeMIMEHeader("=?gb2312?B?suLK1NPKvP4=?=")
	if got != "测试邮件" {
		t.Fatalf("gb2312 subject=%q want 测试邮件", got)
	}
}

func TestDecodeMIMEHeaderGBKAndBig5(t *testing.T) {
	if got := decodeMIMEHeader("=?gbk?B?suLK1NPKvP4=?="); got != "测试邮件" {
		t.Fatalf("gbk subject=%q", got)
	}
	if got := decodeMIMEHeader("=?big5?B?rtQ=?="); got != "朗" {
		t.Fatalf("big5 subject=%q", got)
	}
}

func TestDecodeMIMEHeaderMixedParts(t *testing.T) {
	got := decodeMIMEHeader("Re: =?gb2312?B?suLK1NPKvP4=?= (invoice)")
	if got != "Re: 测试邮件 (invoice)" {
		t.Fatalf("mixed subject=%q", got)
	}
}

// ASCII encoded-words keep working and undecodable input is returned verbatim.
func TestDecodeMIMEHeaderUTF8AndFallback(t *testing.T) {
	if got := decodeMIMEHeader("=?utf-8?B?SGVsbG8=?="); got != "Hello" {
		t.Fatalf("utf-8 subject=%q", got)
	}
	raw := "=?x-unknown-charset?B?AAAA?="
	if got := decodeMIMEHeader(raw); got != raw {
		t.Fatalf("fallback subject=%q want verbatim", got)
	}
}

// A message whose ENVELOPE date cannot be parsed must not serialize a date
// field at all — the webmail client renders a missing date as "—", while a
// zero time used to surface as 1/1/1.
func TestMessageSummaryZeroDateOmittedInJSON(t *testing.T) {
	summary := MessageSummary{UID: 7, Subject: "no date", Date: envelopeDate(parseIMAPDate("not-a-date"))}
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || containsJSONKey(raw, "date") {
		t.Fatalf("zero date serialized: %s", raw)
	}
	withDate := MessageSummary{UID: 8, Date: envelopeDate(time.Unix(1700000000, 0).UTC())}
	raw, err = json.Marshal(withDate)
	if err != nil {
		t.Fatal(err)
	}
	if !containsJSONKey(raw, "date") {
		t.Fatalf("real date missing: %s", raw)
	}
}

func containsJSONKey(raw []byte, key string) bool {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}

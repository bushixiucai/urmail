package billing

import (
	"testing"
	"time"
)

func TestQuoteBindsCanonicalEntitlementAndDocumentSnapshots(t *testing.T) {
	plan := []byte(`{"code":"pro","quota_bytes":1000}`)
	doc := []byte(`{"seller":{"name":"Example"},"total_cents":100}`)
	key := []byte("a-snapshot-signing-key-with-at-least-32-bytes")
	now := time.Now().UTC()
	quote, err := BuildPricedQuote(1, "", "pro", 100, CycleMonthly, "CNY", "", 0, 0, now, now.AddDate(0, 1, 0), time.Hour, key)
	if err != nil {
		t.Fatal(err)
	}
	quote.PlanSnapshotDigest, err = SnapshotDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	quote.DocumentSnapshotDigest, err = SnapshotDigest(doc)
	if err != nil {
		t.Fatal(err)
	}
	quote.Digest = quote.ComputeDigest(key)
	if err = quote.VerifySnapshots([]byte(`{ "quota_bytes": 1000, "code": "pro" }`), doc); err != nil {
		t.Fatalf("PostgreSQL-style key ordering changed facts: %v", err)
	}
	if err = quote.VerifySnapshots([]byte(`{"code":"pro","quota_bytes":1000000}`), doc); err == nil {
		t.Fatal("poisoned entitlement snapshot was accepted")
	}
	if err = quote.VerifySnapshots(plan, []byte(`{"seller":{"name":"Someone else"},"total_cents":100}`)); err == nil {
		t.Fatal("poisoned billing identity was accepted")
	}
	quote.PlanSnapshotDigest = "altered"
	if err = quote.VerifySignature(key); err == nil {
		t.Fatal("snapshot binding was outside the quote HMAC")
	}
	if _, err = SnapshotDigest([]byte(`{} {}`)); err == nil {
		t.Fatal("trailing snapshot data was accepted")
	}
}

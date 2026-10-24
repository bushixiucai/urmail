package mailboxprincipal

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"
	"time"
)

func TestBridgeSignatureBindsEveryRequestComponent(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	now := time.Now()
	stamp := strconv.FormatInt(now.Unix(), 10)
	nonce := "abcdef0123456789abcdef0123456789"
	body := []byte(`{"action":"login"}`)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("POST\n/bridge\n" + stamp + "\n" + nonce + "\n"))
	_, _ = mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))
	if VerifyRequest(key, "/bridge", stamp, nonce, signature, body, now) != nil {
		t.Fatal("valid signature rejected")
	}
	for _, change := range []struct {
		path, stamp, nonce string
		body               []byte
	}{{"/other", stamp, nonce, body}, {"/bridge", strconv.FormatInt(now.Add(-61*time.Second).Unix(), 10), nonce, body}, {"/bridge", stamp, nonce + "a", body}, {"/bridge", stamp, nonce, []byte(`{"action":"logout"}`)}} {
		if VerifyRequest(key, change.path, change.stamp, change.nonce, signature, change.body, now) == nil {
			t.Fatal("changed request authenticated")
		}
	}
}

func TestScopedMailboxReferences(t *testing.T) {
	if !ValidReference("ai_context", "INBOX", 1, 2) || !ValidReference("sieve", "", 0, 0) {
		t.Fatal("valid reference rejected")
	}
	for _, ref := range []struct {
		scope, folder string
		uid, validity int64
	}{{"ai_context", "INBOX", 0, 2}, {"ai_context", "INBOX", 1, 0}, {"ai_context", "INBOX\r", 1, 2}, {"sieve", "INBOX", 1, 2}, {"panel", "", 0, 0}} {
		if ValidReference(ref.scope, ref.folder, ref.uid, ref.validity) {
			t.Fatal("ambiguous reference accepted")
		}
	}
}

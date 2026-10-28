// Package mailboxprincipal authenticates the server-side Roundcube bridge.
// A mailbox principal is independent of any panel user or administrative role.
package mailboxprincipal

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrUnauthorized rejects unauthenticated service requests.
var ErrUnauthorized = errors.New("mailbox bridge authorization failed")

// VerifyRequest authenticates the exact HTTP body, route, timestamp and nonce.
// Callers must persist the verified nonce atomically before performing work.
func VerifyRequest(key []byte, path, timestamp, nonce, signature string, body []byte, now time.Time) error {
	if len(key) < 32 || len(nonce) < 32 || len(nonce) > 128 || strings.ContainsAny(nonce, "\r\n") {
		return ErrUnauthorized
	}
	for _, r := range nonce {
		if (r < 'a' || r > 'f') && (r < '0' || r > '9') {
			return ErrUnauthorized
		}
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || seconds < now.Unix()-60 || seconds > now.Unix()+60 {
		return ErrUnauthorized
	}
	actual, err := hex.DecodeString(signature)
	if err != nil || len(actual) != sha256.Size {
		return ErrUnauthorized
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("POST\n" + path + "\n" + timestamp + "\n" + nonce + "\n"))
	_, _ = mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), actual) {
		return ErrUnauthorized
	}
	return nil
}

// NewToken returns an opaque bearer and the only representation stored in PG.
func NewToken() (string, string, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", "", err
	}
	token := hex.EncodeToString(random[:])
	return token, HashToken(token), nil
}

// HashToken derives the persisted identifier without retaining a bearer.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ValidReference rejects ambiguous folder/UID and scope combinations.
func ValidReference(scope, folder string, uid, validity int64) bool {
	switch scope {
	case "ai_compose":
		return folder == "" && uid == 0 && validity == 0
	case "sieve":
		return folder == "" && uid == 0 && validity == 0
	case "ai_context":
		return len(folder) > 0 && len(folder) <= 255 && !strings.ContainsAny(folder, "\x00\r\n") && uid > 0 && uid <= 4294967295 && validity > 0 && validity <= 4294967295
	default:
		return false
	}
}

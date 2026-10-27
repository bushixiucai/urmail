package restore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

func digest(v string) string { sum := sha256.Sum256([]byte(v)); return hex.EncodeToString(sum[:]) }

func TestVerifyRejectsMissingFile(t *testing.T) {
	body := "mail"
	err := Verify([]File{{Path: "a.eml", SHA256: digest(body), Size: int64(len(body))}}, map[string][]byte{})
	if !errors.Is(err, ErrIncomplete) {
		t.Fatal(err)
	}
}

func TestVerifyAcceptsExactFiles(t *testing.T) {
	body := "mail"
	err := Verify([]File{{Path: "a.eml", SHA256: digest(body), Size: int64(len(body))}}, map[string][]byte{"a.eml": []byte(body)})
	if err != nil {
		t.Fatal(err)
	}
}

func TestVerifyDistinguishesDigestMismatch(t *testing.T) {
	err := Verify([]File{{Path: "a.eml", SHA256: digest("mail"), Size: 4}}, map[string][]byte{"a.eml": []byte("maii")})
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatal(err)
	}
}

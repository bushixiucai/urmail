package mail

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"

	"github.com/GehirnInc/crypt/sha512_crypt"

	"github.com/bushixiucai/urmail/internal/domain/passwordwork"
)

// Dovecot's SQL auth is configured with "default_pass_scheme = SHA512-CRYPT"
// (see internal/mail/dovecot/render.go), so stored mailbox passwords must be
// real glibc SHA512-CRYPT hashes:
//
//	{SHA512-CRYPT}$6$<salt>$<86-char crypt body>
//
// or with explicit rounds:
//
//	{SHA512-CRYPT}$6$rounds=<N>$<salt>$<86-char crypt body>
const (
	// hashRounds is the fixed cost parameter; it matches the glibc default.

	// sha512CryptBodyLen is the length of the base64-encoded crypt body:
	// 64 digest bytes encode to 86 characters in the SHA-crypt alphabet.
	sha512CryptBodyLen = 86

	// glibc limits the salt to 16 characters.
	maxSaltLen = 16
)

// cryptBase64 is the SHA-crypt base64 alphabet ("./0-9A-Za-z"); salts must
// use these characters so that Dovecot/glibc parse them unambiguously.
const cryptBase64 = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// hashPreamble is the Dovecot scheme prefix prepended to the crypt hash.
const hashPreamble = "{SHA512-CRYPT}"

// HashPassword produces a Dovecot-compatible password hash:
// {SHA512-CRYPT}$6$<salt>$<hash> with a random 16-character salt and 5000
// rounds of the SHA512-CRYPT algorithm.
func HashPassword(password string) (string, error) {
	return HashPasswordContext(context.Background(), password)
}

// HashPasswordContext rejects canceled or excessive work before computing.
func HashPasswordContext(ctx context.Context, password string) (string, error) {
	if !passwordwork.ValidVerification(password) || len(password) > passwordwork.MaxNewBytes {
		return "", passwordwork.ErrInvalidPassword
	}
	release, err := passwordwork.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()

	buf := make([]byte, maxSaltLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	salt := make([]byte, maxSaltLen)
	for i, b := range buf {
		salt[i] = cryptBase64[int(b)%len(cryptBase64)]
	}

	body, err := sha512_crypt.New().Generate([]byte(password), []byte("$6$"+string(salt)+"$"))
	if err != nil {
		return "", fmt.Errorf("sha512-crypt: %w", err)
	}
	return hashPreamble + body, nil
}

// VerifyPassword checks whether a password matches a stored
// {SHA512-CRYPT}$6$... hash. It re-derives the digest from the salt and rounds
// recorded in the stored hash and compares the bodies in constant time
// (hmac.Equal). Malformed, truncated or unsupported hashes return false
// without panicking.
func VerifyPassword(password, storedHash string) bool {
	ok, _ := VerifyPasswordContext(context.Background(), password, storedHash)
	return ok
}

// VerifyPasswordContext bounds CPU work without creating detached goroutines.
func VerifyPasswordContext(ctx context.Context, password, storedHash string) (bool, error) {
	if !passwordwork.ValidVerification(password) {
		return false, nil
	}
	release, err := passwordwork.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	return verifyPassword(password, storedHash), nil
}

func verifyPassword(password, storedHash string) bool {
	raw, ok := strings.CutPrefix(storedHash, hashPreamble)
	if !ok {
		return false
	}

	// Re-derive with the salt/rounds embedded in the stored setting, then
	// compare bodies ourselves so the comparison is constant-time.
	// The stored hash starts with "$6$", so the split yields a leading empty
	// element; drop it before validating the scheme field.
	parts := strings.Split(strings.TrimPrefix(raw, "$"), "$")
	if len(parts) < 3 || parts[0] != "6" {
		return false
	}
	rest := parts[1:]
	setting := "$6$"
	if r, ok := strings.CutPrefix(rest[0], "rounds="); ok {
		n, err := strconv.Atoi(r)
		if err != nil || n < 1000 || n > 100000 || len(rest) < 3 {
			return false
		}
		setting += "rounds=" + r + "$"
		rest = rest[1:]
	}
	if len(rest) != 2 {
		return false
	}
	salt, body := rest[0], rest[1]
	if salt == "" || len(salt) > maxSaltLen || len(body) != sha512CryptBodyLen {
		return false
	}

	// The library accepts the setting with or without a trailing "$"; passing
	// it without avoids an empty extra field in the derived hash.
	derived, err := sha512_crypt.New().Generate([]byte(password), []byte(setting+salt))
	if err != nil {
		return false
	}
	derivedParts := strings.Split(derived, "$")
	if len(derivedParts) < 2 {
		return false
	}
	derivedBody := derivedParts[len(derivedParts)-1]
	if len(derivedBody) != len(body) {
		return false
	}
	// hmac.Equal runs in constant time for equal-length inputs, which the
	// length checks above guarantee.
	return hmac.Equal([]byte(derivedBody), []byte(body))
}

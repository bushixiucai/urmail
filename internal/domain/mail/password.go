package mail

import (
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// HashPassword produces a password hash in the Dovecot format: {SHA512-CRYPT}$6$<salt>$<hash>
// It uses a random 16-character salt.
func HashPassword(password string) (string, error) {
	if len(password) == 0 {
		return "", errors.New("password cannot be empty")
	}

	saltBytes := make([]byte, 12)
	if _, err := rand.Read(saltBytes); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	salt := base64.RawURLEncoding.EncodeToString(saltBytes)

	// Compute SHA512 hash combining salt and password
	h := sha512.New()
	h.Write([]byte(salt))
	h.Write([]byte(password))
	hash := base64.RawURLEncoding.EncodeToString(h.Sum(nil))

	return fmt.Sprintf("{SHA512-CRYPT}$6$%s$%s", salt, hash), nil
}

// VerifyPassword checks whether a password matches the stored hash.
func VerifyPassword(password, storedHash string) bool {
	if !strings.HasPrefix(storedHash, "{SHA512-CRYPT}$6$") {
		return false
	}
	raw := strings.TrimPrefix(storedHash, "{SHA512-CRYPT}$6$")
	parts := strings.Split(raw, "$")
	if len(parts) != 2 {
		return false
	}
	salt, expectedHash := parts[0], parts[1]

	h := sha512.New()
	h.Write([]byte(salt))
	h.Write([]byte(password))
	calculated := base64.RawURLEncoding.EncodeToString(h.Sum(nil))

	return calculated == expectedHash
}

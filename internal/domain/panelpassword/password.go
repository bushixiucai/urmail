// Package panelpassword stores panel credentials independently of Dovecot.
package panelpassword

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/domain/passwordwork"
)

const memory = 32 * 1024
const iterations = 2
const parallelism = 1

// Hash creates a bounded Argon2id verifier for a newly chosen panel password.
func Hash(password string) (string, error) { return HashContext(context.Background(), password) }

// HashContext rejects canceled or saturated computation.
func HashContext(ctx context.Context, password string) (string, error) {
	if err := passwordwork.ValidateNew(password); err != nil {
		return "", err
	}
	return hashExisting(ctx, password)
}

// Rehash preserves a valid historical password without applying new-password policy.
func Rehash(ctx context.Context, password string) (string, error) {
	if !passwordwork.ValidVerification(password) {
		return "", passwordwork.ErrInvalidPassword
	}
	return hashExisting(ctx, password)
}

func hashExisting(ctx context.Context, password string) (string, error) {
	release, err := passwordwork.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	salt := make([]byte, 16)
	if _, err = rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", memory, iterations, parallelism, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify supports bounded legacy SHA512-CRYPT during gradual migration.
func Verify(password, hash string) bool {
	ok, _ := VerifyContext(context.Background(), password, hash)
	return ok
}

// VerifyContext supports legacy and modern hashes under the same shared budget.
func VerifyContext(ctx context.Context, password, hash string) (bool, error) {
	if !passwordwork.ValidVerification(password) {
		return false, nil
	}
	if strings.HasPrefix(hash, "{SHA512-CRYPT}") {
		return mail.VerifyPasswordContext(ctx, password, hash)
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false, nil
	}
	var m, t, p uint64
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return false, nil
	}
	for i, param := range params {
		pair := strings.Split(param, "=")
		if len(pair) != 2 {
			return false, nil
		}
		n, e := strconv.ParseUint(pair[1], 10, 32)
		if e != nil {
			return false, nil
		}
		switch i {
		case 0:
			if pair[0] != "m" {
				return false, nil
			}
			m = n
		case 1:
			if pair[0] != "t" {
				return false, nil
			}
			t = n
		case 2:
			if pair[0] != "p" {
				return false, nil
			}
			p = n
		}
	}
	if m != memory || t != iterations || p != parallelism {
		return false, nil
	}
	salt, e := base64.RawStdEncoding.DecodeString(parts[4])
	if e != nil || len(salt) != 16 {
		return false, nil
	}
	want, e := base64.RawStdEncoding.DecodeString(parts[5])
	if e != nil || len(want) != 32 {
		return false, nil
	}
	release, e := passwordwork.Acquire(ctx)
	if e != nil {
		return false, e
	}
	defer release()
	key := argon2.IDKey([]byte(password), salt, uint32(t), uint32(m), uint8(p), 32)
	return subtle.ConstantTimeCompare(want, key) == 1, nil
}

// NeedsUpgrade identifies legacy verifiers without changing credential identity.
func NeedsUpgrade(hash string) bool { return strings.HasPrefix(hash, "{SHA512-CRYPT}") }

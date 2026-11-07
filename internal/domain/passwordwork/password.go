// Package passwordwork bounds password inputs and shared expensive work.
package passwordwork

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Password limits apply before all expensive verifier operations.
const (
	MinNewRunes    = 12
	MaxNewBytes    = 256
	MaxVerifyBytes = 4096
	MaxConcurrent  = 4
)

// Password errors distinguish invalid input from exhausted computation capacity.
var (
	ErrInvalidPassword = errors.New("password does not meet the byte length policy")
	ErrBusy            = errors.New("password computation capacity exhausted")
	slots              = make(chan struct{}, MaxConcurrent)
)

// ValidateNew applies to newly chosen passwords, not historical verification.
// Control characters are rejected the same way mailbox passwords are: they
// corrupt logs and terminal-based entry flows.
func ValidateNew(password string) error {
	if utf8.RuneCountInString(password) < MinNewRunes || len(password) > MaxNewBytes || !utf8.ValidString(password) {
		return ErrInvalidPassword
	}
	if strings.ContainsFunc(password, unicode.IsControl) {
		return ErrInvalidPassword
	}
	return nil
}

// ValidVerification rejects excessive inputs before any password computation.
func ValidVerification(password string) bool {
	return len(password) > 0 && len(password) <= MaxVerifyBytes && utf8.ValidString(password)
}

// Acquire never queues unbounded work and observes already canceled requests.
func Acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	default:
		return nil, ErrBusy
	}
}

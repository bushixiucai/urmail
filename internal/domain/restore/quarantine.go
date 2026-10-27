package restore

import (
	"context"
	"errors"
)

// ErrQuarantine is the quarantine error.
var ErrQuarantine = errors.New("restore quarantine failed")

// Begin quarantines the target database before an overwrite.
type Begin func(context.Context) error

// BeforeOverwrite implements the before overwrite step.
func BeforeOverwrite(ctx context.Context, begin Begin, overwrite func(context.Context) error) error {
	if begin == nil || overwrite == nil {
		return ErrQuarantine
	}
	if err := begin(ctx); err != nil {
		return ErrQuarantine
	}
	return overwrite(ctx)
}

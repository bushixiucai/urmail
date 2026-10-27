package restore

import (
	"context"
	"errors"
)

// ErrReleaseBlocked is the release blocked error.
var ErrReleaseBlocked = errors.New("restore release blocked")

// Finish implements the finish step.
func Finish(ctx context.Context, steps []func(context.Context) error, release func(context.Context) error) error {
	if release == nil {
		return ErrReleaseBlocked
	}
	for _, step := range steps {
		if step == nil {
			return ErrReleaseBlocked
		}
		if err := step(ctx); err != nil {
			return err
		}
	}
	return release(ctx)
}

// Package evidence implements domain rules for evidence.
package evidence

import (
	"context"
	"errors"
)

// ErrNotVisible is the not visible error.
var ErrNotVisible = errors.New("evidence not visible")

// Visible implements the corresponding domain rule.
type Visible func(context.Context, int64, int64, int64, string) (bool, error)

// Read implements the corresponding domain rule.
func Read(ctx context.Context, visible Visible, tenant, request, actor int64, digest, manifest string) (string, error) {
	if visible == nil || len(digest) != 64 || manifest == "" {
		return "", ErrNotVisible
	}
	ok, err := visible(ctx, tenant, request, actor, digest)
	if err != nil || !ok {
		return "", ErrNotVisible
	}
	return manifest, nil
}

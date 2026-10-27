// Package evidence implements domain rules for evidence.
package evidence

import (
	"context"
	"encoding/json"
)

// BodyStore implements the corresponding domain rule.
type BodyStore interface {
	Save(context.Context, int64, int64, string, []byte) error
}

// StoreBody implements the corresponding domain rule.
func StoreBody(ctx context.Context, store BodyStore, tenant, request int64, approvedDigest string, manifest Manifest) error {
	if store == nil || manifest.Digest != approvedDigest {
		return ErrMismatch
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	sum := hash(body)
	if sum != approvedDigest {
		return ErrMismatch
	}
	return store.Save(ctx, tenant, request, approvedDigest, body)
}

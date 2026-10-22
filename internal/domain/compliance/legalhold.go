package compliance

import (
	"context"
	"errors"
)

// ErrLegalHold is returned when an operation would touch an archive entry
// that is under legal hold (M3 review P2-3).
var ErrLegalHold = errors.New("archive entry is under legal hold")

// LegalHoldReader abstracts the store lookup for an entry's legal-hold flag
// (store.GetArchiveEntryLegalHold).
type LegalHoldReader interface {
	GetArchiveEntryLegalHold(ctx context.Context, tenantID, entryID int64) (bool, error)
}

// LegalHoldActive reports whether the archive entry is under legal hold.
// A missing entry is reported as not held; callers that care about existence
// should use the store query directly.
func LegalHoldActive(ctx context.Context, r LegalHoldReader, tenantID, entryID int64) (bool, error) {
	held, err := r.GetArchiveEntryLegalHold(ctx, tenantID, entryID)
	if err != nil {
		return false, err
	}
	return held, nil
}

// EnsureDeletable returns ErrLegalHold when the entry must not be deleted,
// nil when deletion may proceed.
func EnsureDeletable(ctx context.Context, r LegalHoldReader, tenantID, entryID int64) error {
	held, err := LegalHoldActive(ctx, r, tenantID, entryID)
	if err != nil {
		return err
	}
	if held {
		return ErrLegalHold
	}
	return nil
}

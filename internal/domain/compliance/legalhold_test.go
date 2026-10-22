package compliance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bushixiucai/urmail/internal/domain/compliance"
)

type fakeLegalHoldReader struct {
	held map[int64]bool
	err  error
}

func (f *fakeLegalHoldReader) GetArchiveEntryLegalHold(ctx context.Context, tenantID, entryID int64) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.held[entryID], nil
}

func TestLegalHoldActive(t *testing.T) {
	ctx := context.Background()
	r := &fakeLegalHoldReader{held: map[int64]bool{1: true, 2: false}}

	held, err := compliance.LegalHoldActive(ctx, r, 10, 1)
	if err != nil || !held {
		t.Errorf("entry 1 should be on hold, got held=%v err=%v", held, err)
	}
	held, err = compliance.LegalHoldActive(ctx, r, 10, 2)
	if err != nil || held {
		t.Errorf("entry 2 should not be on hold, got held=%v err=%v", held, err)
	}
	// Unknown entry: reported as not held, no error.
	held, err = compliance.LegalHoldActive(ctx, r, 10, 99)
	if err != nil || held {
		t.Errorf("unknown entry should read as not held, got held=%v err=%v", held, err)
	}
}

func TestEnsureDeletable(t *testing.T) {
	ctx := context.Background()
	r := &fakeLegalHoldReader{held: map[int64]bool{1: true}}

	if err := compliance.EnsureDeletable(ctx, r, 10, 1); !errors.Is(err, compliance.ErrLegalHold) {
		t.Errorf("held entry must not be deletable, got %v", err)
	}
	if err := compliance.EnsureDeletable(ctx, r, 10, 2); err != nil {
		t.Errorf("unheld entry should be deletable, got %v", err)
	}
}

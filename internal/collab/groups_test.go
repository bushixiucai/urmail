package collab

import (
	"context"
	"errors"
	"testing"

	domain "github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
)

type groupStore struct{ count int64 }

// PlatformInsertContactGroupMember implements the platform insert contact group member step.
func (g groupStore) PlatformInsertContactGroupMember(context.Context, store.PlatformInsertContactGroupMemberParams) (int64, error) {
	return g.count, nil
}

// TestAddGroupMemberRejectsZeroRows implements the test add group member rejects zero rows step.
func TestAddGroupMemberRejectsZeroRows(t *testing.T) {
	err := AddGroupMember(context.Background(), groupStore{count: 0}, Principal{TenantID: 1, MailboxID: 2}, 3, 4)
	if !errors.Is(err, domain.ErrGroupScope) {
		t.Fatal(err)
	}
}

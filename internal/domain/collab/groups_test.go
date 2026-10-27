package collab

import (
	"errors"
	"testing"
)

func TestMemberRejectsAnotherAddressBook(t *testing.T) {
	_, err := Member(1, 2, 3, 4)
	if !errors.Is(err, ErrGroupScope) {
		t.Fatal(err)
	}
}

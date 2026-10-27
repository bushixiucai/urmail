// Package collab implements domain rules for collab.
package collab

import "errors"

// ErrGroupScope is the group scope error.
var ErrGroupScope = errors.New("contact group scope mismatch")

// GroupMember implements the corresponding domain rule.
type GroupMember struct{ AddressbookID, GroupID, ContactID int64 }

// Member implements the corresponding domain rule.
func Member(groupBook, contactBook, groupID, contactID int64) (GroupMember, error) {
	if groupBook <= 0 || groupBook != contactBook || groupID <= 0 || contactID <= 0 {
		return GroupMember{}, ErrGroupScope
	}
	return GroupMember{AddressbookID: groupBook, GroupID: groupID, ContactID: contactID}, nil
}

// Package collab implements domain rules for collab.
package collab

import "context"

// SaveEvent implements the corresponding domain rule.
func SaveEvent(_ context.Context, event Event, send InviteSender) (Event, bool, error) {
	norm, err := NormalizeEvent(event)
	if err != nil {
		return Event{}, false, err
	}
	_ = send
	return norm, false, nil
}

// SendInvitations delivers one confirmed invitation. Without the explicit
// confirmation, without a sender, without recipients, or with an event that
// contradicts the requested method, nothing is sent and the invitation is
// refused: mail only ever goes out on purpose.
func SendInvitations(ctx context.Context, inv Invitation, confirmed bool, send InviteSender) error {
	if !confirmed || send == nil {
		return ErrInvalidEvent
	}
	if _, err := InvitationICS(inv); err != nil {
		return err
	}
	return send(ctx, inv)
}

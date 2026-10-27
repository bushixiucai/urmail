// Package marketingflow implements the pure advance rules for marketing flow
// positions: which step fires next, what the worker must persist, and when a
// send is not allowed or ended uncertain.
package marketingflow

import (
	"context"
	"errors"
)

// ErrNotAllowed is returned when the position is not eligible to send.
var ErrNotAllowed = errors.New("marketing flow not allowed")

// ErrUncertain is returned when the send outcome cannot be proven; the
// position is parked for bounded at-least-once retry.
var ErrUncertain = errors.New("marketing delivery uncertain")

// Position is the persisted advance state for one recipient.
type Position struct {
	Step         string
	Unsubscribed bool
}

// Store persists and loads flow positions.
type Store interface {
	MayAdvance(context.Context, int64, int64, string) (bool, error)
	Position(context.Context, int64, int64, string) (Position, error)
	Save(context.Context, int64, int64, string, Position) error
	MarkUncertain(context.Context, int64, int64, string) error
	// StillAllowed is the execution-time re-verification: entitlement,
	// suppression and unsubscribe are re-checked immediately before the
	// sender runs, even when the step batch was already claimed.
	StillAllowed(context.Context, int64, int64, string) (bool, error)
}

// Sender performs the actual flow message submission.
type Sender func(context.Context, int64, int64, string, string) error

// Advance applies one flow event. An unsubscribe recorded between the claim
// and the send is terminal: the current and every subsequent step stop and
// the message is never sent.
func Advance(ctx context.Context, store Store, sender Sender, tenant, flow int64, email, event, next string) error {
	allowed, err := store.MayAdvance(ctx, tenant, flow, email)
	if err != nil {
		return err
	}
	current, err := store.Position(ctx, tenant, flow, email)
	if err != nil {
		return err
	}
	if current.Unsubscribed || !allowed {
		current.Unsubscribed = current.Unsubscribed || event == "unsubscribed"
		return store.Save(ctx, tenant, flow, email, current)
	}
	if event == "send" {
		if sender == nil {
			return ErrNotAllowed
		}
		stillAllowed, err := store.StillAllowed(ctx, tenant, flow, email)
		if err != nil {
			return err
		}
		if !stillAllowed {
			current.Unsubscribed = true
			return store.Save(ctx, tenant, flow, email, current)
		}
		if err = sender(ctx, tenant, flow, email, current.Step); err != nil {
			if errors.Is(err, ErrUncertain) {
				if markErr := store.MarkUncertain(ctx, tenant, flow, email); markErr != nil {
					return markErr
				}
				return ErrUncertain
			}
			return err
		}
	}
	if event == "unsubscribed" {
		current.Unsubscribed = true
	}
	if next != "" {
		current.Step = next
	}
	return store.Save(ctx, tenant, flow, email, current)
}

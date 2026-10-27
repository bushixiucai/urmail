package marketingflow

import (
	"context"
	"errors"
	"testing"
)

type fake struct {
	allowed    bool
	stillOK    bool
	current    Position
	sent       int
	marked     int
	savedCount int
}

func (f *fake) MayAdvance(context.Context, int64, int64, string) (bool, error) { return f.allowed, nil }
func (f *fake) Position(context.Context, int64, int64, string) (Position, error) {
	return f.current, nil
}
func (f *fake) Save(_ context.Context, _, _ int64, _ string, p Position) error {
	f.current = p
	f.savedCount++
	return nil
}
func (f *fake) MarkUncertain(context.Context, int64, int64, string) error { f.marked++; return nil }

// StillAllowed models an unsubscribe landing after the claim but before the
// sender runs; the flip is observed only on the execution-time recheck.
func (f *fake) StillAllowed(context.Context, int64, int64, string) (bool, error) {
	return f.stillOK, nil
}

func TestBlockedFlowDoesNotSend(t *testing.T) {
	f := &fake{allowed: false, stillOK: true, current: Position{Step: "welcome"}}
	sender := func(context.Context, int64, int64, string, string) error { f.sent++; return nil }
	if err := Advance(context.Background(), f, sender, 1, 2, "a@example.test", "send", "next"); err != nil {
		t.Fatal(err)
	}
	if f.sent != 0 || f.current.Step != "welcome" {
		t.Fatalf("%+v", f)
	}
}

func TestAllowedFlowSendsOnce(t *testing.T) {
	f := &fake{allowed: true, stillOK: true, current: Position{Step: "welcome"}}
	sender := func(context.Context, int64, int64, string, string) error { f.sent++; return nil }
	if err := Advance(context.Background(), f, sender, 1, 2, "a@example.test", "send", "next"); err != nil || f.sent != 1 || f.current.Step != "next" {
		t.Fatalf("%+v %v", f, err)
	}
}

func TestUncertainSendDoesNotAdvance(t *testing.T) {
	f := &fake{allowed: true, stillOK: true, current: Position{Step: "welcome"}}
	sender := func(context.Context, int64, int64, string, string) error { f.sent++; return ErrUncertain }
	err := Advance(context.Background(), f, sender, 1, 2, "a@example.test", "send", "next")
	if !errors.Is(err, ErrUncertain) || f.sent != 1 || f.marked != 1 || f.current.Step != "welcome" {
		t.Fatalf("%+v %v", f, err)
	}
}

// TestUnsubscribeBetweenClaimAndSendStopsEveryStep proves the execution-time
// recheck: even though the step batch was claimed (MayAdvance true), an
// unsubscribe recorded before the send marks the position terminal so this
// and every subsequent step stop without sending.
func TestUnsubscribeBetweenClaimAndSendStopsEveryStep(t *testing.T) {
	f := &fake{allowed: true, current: Position{Step: "welcome"}}
	sender := func(context.Context, int64, int64, string, string) error { f.sent++; return nil }
	if err := Advance(context.Background(), f, sender, 1, 2, "a@example.test", "send", "next"); err != nil {
		t.Fatal(err)
	}
	if f.sent != 0 {
		t.Fatal("message sent after unsubscribe")
	}
	if !f.current.Unsubscribed || f.current.Step != "welcome" {
		t.Fatalf("position not terminal: %+v", f.current)
	}
	// A subsequent claimed batch for the same recipient can never recover.
	if err := Advance(context.Background(), f, sender, 1, 2, "a@example.test", "send", "later"); err != nil {
		t.Fatal(err)
	}
	if f.sent != 0 || f.current.Step != "welcome" {
		t.Fatalf("subsequent step advanced after unsubscribe: %+v", f.current)
	}
}

func TestStillAllowedCheckFailureAbortsSend(t *testing.T) {
	f := &fake{allowed: true, current: Position{Step: "welcome"}}
	sender := func(context.Context, int64, int64, string, string) error { f.sent++; return nil }
	broken := storeFuncs{may: func() (bool, error) { return true, nil }, pos: func() (Position, error) { return Position{Step: "welcome"}, nil }, still: func() (bool, error) { return false, errors.New("recheck unavailable") }}
	err := Advance(context.Background(), broken, sender, 1, 2, "a@example.test", "send", "next")
	if err == nil || f.sent != 0 {
		t.Fatalf("send continued with broken recheck: sent=%d err=%v", f.sent, err)
	}
}

type storeFuncs struct {
	may   func() (bool, error)
	pos   func() (Position, error)
	still func() (bool, error)
}

func (s storeFuncs) MayAdvance(context.Context, int64, int64, string) (bool, error) { return s.may() }
func (s storeFuncs) Position(context.Context, int64, int64, string) (Position, error) {
	return s.pos()
}
func (storeFuncs) Save(context.Context, int64, int64, string, Position) error { return nil }
func (storeFuncs) MarkUncertain(context.Context, int64, int64, string) error  { return nil }
func (s storeFuncs) StillAllowed(context.Context, int64, int64, string) (bool, error) {
	return s.still()
}

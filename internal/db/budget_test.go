package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

func testAdmission() *admission {
	return newAdmission(Budget{MaxConns: 1, MaxQueue: 1, AcquireTimeout: 20 * time.Millisecond, OperationTimeout: time.Second})
}
func TestBudgetQueueExhaustionAndTimeout(t *testing.T) {
	a := testAdmission()
	defer a.cancel()
	_, release, err := a.enter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	a.queued <- struct{}{}
	start := time.Now()
	_, _, err = a.enter(context.Background())
	if !errors.Is(err, ErrBudgetExhausted) || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("rejection %v", err)
	}
	<-a.queued
	start = time.Now()
	_, _, err = a.enter(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("timeout %v", err)
	}
	if len(a.queued) != 0 {
		t.Fatal("queue leaked")
	}
}
func TestBudgetCancellationCloseAndIsolation(t *testing.T) {
	a, b := testAdmission(), testAdmission()
	defer b.cancel()
	op, release, err := a.enter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, other, err := b.enter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	other()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err = a.enter(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	a.cancel()
	select {
	case <-op.Done():
	case <-time.After(time.Second):
		t.Fatal("active operation not canceled")
	}
	if _, _, err = a.enter(context.Background()); !errors.Is(err, ErrPoolClosed) {
		t.Fatal(err)
	}
	release()
	release()
	if len(a.active) != 0 {
		t.Fatal("permit leaked")
	}
}
func TestBudgetOperationDeadline(t *testing.T) {
	a := testAdmission()
	defer a.cancel()
	a.budget.OperationTimeout = time.Millisecond
	ctx, release, err := a.enter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("deadline absent")
	}
}
func TestBudgetCloseWakesQueuedCaller(t *testing.T) {
	a := testAdmission()
	a.budget.AcquireTimeout = time.Hour
	_, release, _ := a.enter(context.Background())
	defer release()
	result := make(chan error, 1)
	go func() {
		_, done, err := a.enter(context.Background())
		if done != nil {
			done()
		}
		result <- err
	}()
	a.cancel()
	select {
	case err := <-result:
		if !errors.Is(err, ErrPoolClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("queue not woken")
	}
}

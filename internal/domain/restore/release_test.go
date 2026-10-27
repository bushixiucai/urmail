package restore

import (
	"context"
	"errors"
	"testing"
)

func TestFinishDoesNotReleaseAfterFailure(t *testing.T) {
	released := false
	err := Finish(context.Background(), []func(context.Context) error{func(context.Context) error { return errors.New("failed") }}, func(context.Context) error { released = true; return nil })
	if err == nil || released {
		t.Fatal(err, released)
	}
}

func TestFinishReleasesAfterSuccess(t *testing.T) {
	released := false
	err := Finish(context.Background(), []func(context.Context) error{func(context.Context) error { return nil }}, func(context.Context) error { released = true; return nil })
	if err != nil || !released {
		t.Fatal(err, released)
	}
}

package restore

import (
	"context"
	"errors"
	"testing"
)

func TestOverwriteDoesNotRunWhenQuarantineFails(t *testing.T) {
	called := false
	err := BeforeOverwrite(context.Background(), func(context.Context) error { return errors.New("down") }, func(context.Context) error { called = true; return nil })
	if !errors.Is(err, ErrQuarantine) || called {
		t.Fatal(err, called)
	}
}

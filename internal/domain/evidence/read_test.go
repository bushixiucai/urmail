package evidence

import (
	"context"
	"errors"
	"testing"
)

func TestReadRequiresVisibility(t *testing.T) {
	_, err := Read(context.Background(), func(context.Context, int64, int64, int64, string) (bool, error) { return false, nil }, 1, 2, 3, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "manifest")
	if !errors.Is(err, ErrNotVisible) {
		t.Fatal(err)
	}
}

func TestReadReturnsManifestWhenVisible(t *testing.T) {
	got, err := Read(context.Background(), func(context.Context, int64, int64, int64, string) (bool, error) { return true, nil }, 1, 2, 3, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "manifest")
	if err != nil || got != "manifest" {
		t.Fatalf("%s %v", got, err)
	}
}

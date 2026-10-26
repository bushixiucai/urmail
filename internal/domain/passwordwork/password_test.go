package passwordwork

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPasswordPolicyUsesRunesAndBytes(t *testing.T) {
	for _, p := range []string{strings.Repeat("a", 11), strings.Repeat("字", 11), strings.Repeat("字", 86), string([]byte{0xff})} {
		if ValidateNew(p) == nil {
			t.Fatal("invalid new password accepted")
		}
	}
	for _, p := range []string{strings.Repeat("a", 12), strings.Repeat("字", 12), strings.Repeat("a", 256)} {
		if err := ValidateNew(p); err != nil {
			t.Fatal(err)
		}
	}
	if !ValidVerification("short") || ValidVerification(strings.Repeat("a", MaxVerifyBytes+1)) {
		t.Fatal("legacy verification bounds")
	}
}
func TestPasswordWorkNeverQueuesBeyondCapacity(t *testing.T) {
	releases := make([]func(), 0, MaxConcurrent)
	for i := 0; i < MaxConcurrent; i++ {
		release, err := Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	if _, err := Acquire(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatal("saturated capacity did not reject")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled capacity request accepted")
	}
}

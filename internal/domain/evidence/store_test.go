package evidence

import (
	"context"
	"errors"
	"testing"
)

type memoryBodies struct{ saved int }

func (m *memoryBodies) Save(context.Context, int64, int64, string, []byte) error {
	m.saved++
	return nil
}

func TestStoreBodyRejectsDigestMismatch(t *testing.T) {
	m := &memoryBodies{}
	manifest, err := Build("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", []Source{{ID: "a", SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Size: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if err = StoreBody(context.Background(), m, 1, 2, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", manifest); !errors.Is(err, ErrMismatch) || m.saved != 0 {
		t.Fatal(err, m.saved)
	}
}

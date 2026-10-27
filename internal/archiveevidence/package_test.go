package archiveevidence

import (
	"errors"
	"testing"

	"github.com/bushixiucai/urmail/internal/domain/evidence"
)

func TestPackageRejectsChangedOriginal(t *testing.T) {
	original := []byte("original message")
	manifest, err := Package("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", []Original{{ID: "7", Bytes: original}})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"7": append([]byte(nil), original...)}
	if err = evidence.Verify(manifest, files); err != nil {
		t.Fatal(err)
	}
	files["7"][0] = 'X'
	if err = evidence.Verify(manifest, files); !errors.Is(err, evidence.ErrMismatch) {
		t.Fatal(err)
	}
}

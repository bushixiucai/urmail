package evidence

import (
	"errors"
	"testing"
)

func TestVerifyRejectsTamperAndMissingFile(t *testing.T) {
	file := []byte("original")
	manifest, err := Build(hash([]byte("approval")), []Source{{ID: "1", SHA256: hash(file), Size: int64(len(file))}})
	if err != nil {
		t.Fatal(err)
	}
	if err = Verify(manifest, map[string][]byte{"1": file}); err != nil {
		t.Fatal(err)
	}
	if err = Verify(manifest, map[string][]byte{"1": []byte("changed")}); !errors.Is(err, ErrMismatch) {
		t.Fatal(err)
	}
	if err = Verify(manifest, map[string][]byte{}); !errors.Is(err, ErrMismatch) {
		t.Fatal(err)
	}
}

func TestSourceUsesOriginalDigest(t *testing.T) {
	original := []byte("From: a@example.test\r\n\r\nbody")
	source := SourceFromOriginal("42", original)
	manifest, err := Build(hash([]byte("approval")), []Source{source})
	if err != nil {
		t.Fatal(err)
	}
	if err = Verify(manifest, map[string][]byte{"42": original}); err != nil {
		t.Fatal(err)
	}
	if source.SHA256 != hash(original) || source.Size != int64(len(original)) {
		t.Fatalf("%+v", source)
	}
}

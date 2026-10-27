package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/bushixiucai/urmail/internal/backupauto"
)

// fileStore adapts the local directory to the OffsiteStore contract so the
// decrypt test never needs a network.
type fileStore struct{ dir string }

func newFileStore(t *testing.T, dir string) backupauto.OffsiteStore {
	t.Helper()
	return fileStore{dir: dir}
}

func (f fileStore) Upload(_ context.Context, name string, r io.Reader, _ int64) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(f.dir, name), data, 0o600)
}

func (f fileStore) List(context.Context) ([]backupauto.RemoteObject, error) { return nil, nil }

func (f fileStore) Download(_ context.Context, name string, w io.Writer) error {
	data, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func (f fileStore) Delete(context.Context, string) error { return nil }

func jsonMarshalIndentRaw(v any) (string, error) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(raw) + "\n", nil
}

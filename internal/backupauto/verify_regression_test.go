package backupauto

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"
)

type earlyUploadFailure struct{ *memoryStore }

func (s earlyUploadFailure) Upload(context.Context, string, io.Reader, int64) error {
	return errors.New("controlled immediate upload refusal")
}

func TestEncryptUploadRejectsChangedPlaintextAndEarlyStoreFailure(t *testing.T) {
	identity, e := age.GenerateX25519Identity()
	if e != nil {
		t.Fatal(e)
	}
	raw := []byte("original exact backup bytes")
	file := filepath.Join(t.TempDir(), "owned.dump")
	if e = os.WriteFile(file, raw, 0o600); e != nil {
		t.Fatal(e)
	}
	artifact := LocalArtifact{Name: "owned.dump", Path: file, Kind: KindPgDump, Size: int64(len(raw)), SHA256: sha256Hex(raw)}
	changed := artifact
	changed.SHA256 = sha256Hex([]byte("different source"))
	if _, e = EncryptAndUpload(t.Context(), newMemoryStore(), changed, identity.Recipient().String()); e == nil {
		t.Fatal("changed plaintext trusted")
	}
	done := make(chan error, 1)
	go func() {
		_, err := EncryptAndUpload(t.Context(), earlyUploadFailure{newMemoryStore()}, artifact, identity.Recipient().String())
		done <- err
	}()
	select {
	case e = <-done:
		if e == nil {
			t.Fatal("early refusal ignored")
		}
	case <-time.After(time.Second):
		t.Fatal("early upload failure leaked encryption pipe")
	}
}

type stalledRetentionStore struct{ *retentionStoreFixture }

func (s stalledRetentionStore) Download(ctx context.Context, _ string, _ io.Writer) error {
	<-ctx.Done()
	return ctx.Err()
}
func TestRetentionDeadlineRefusesBeforeDeletion(t *testing.T) {
	worker, store, _, _, _ := retentionWorkerFixture(t)
	worker.Store = stalledRetentionStore{store}
	worker.RetentionVerifyTimeout = 30 * time.Millisecond
	started := time.Now()
	n, _, e := worker.applyRetention(t.Context())
	if e == nil || n != 0 || len(store.deletes) != 0 || time.Since(started) > time.Second {
		t.Fatal("deadline authorized deletion or blocked", n, e, time.Since(started))
	}
}

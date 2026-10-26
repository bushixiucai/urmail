package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
)

type repeatedReader struct{ remaining int64 }

func (r *repeatedReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), r.remaining)
	clear(p[:n])
	r.remaining -= n
	return int(n), nil
}

type interruptedReader struct{}

func (interruptedReader) Read([]byte) (int, error) { return 0, errors.New("injected read failure") }

func TestSpoolStreamsLargeOriginalsAndRefusesPartialPublication(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	recipient := domainMail.ArchiveRecipient{TenantID: 1, MailboxID: 2, Address: "alice@example.test"}
	for _, size := range []int64{(64 << 20) + 1, (128 << 20) + 1024} {
		manifest, err := Spool(root, recipient, &repeatedReader{size})
		if err != nil || manifest.Size != size {
			t.Fatalf("size=%d manifest=%+v err=%v", size, manifest, err)
		}
		if manifest.Size > MaxExtraction {
			f, err := os.Open(filepath.Join(root, manifest.ID, "raw.eml"))
			if err != nil {
				t.Fatal(err)
			}
			content := ExtractMIMEReader(f, manifest.Size, manifest.SHA256)
			_ = f.Close()
			if content.Error != "extraction_limit" {
				t.Fatal("large raw must retain explicit optional indexing limit")
			}
		}
	}
	before, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Spool(root, recipient, &repeatedReader{MaxMessage + 1}); err == nil {
		t.Fatal("oversized original accepted")
	}
	if _, err = Spool(root, recipient, interruptedReader{}); err == nil {
		t.Fatal("partial read acknowledged")
	}
	after, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatal("failed spool published partial or leaked pending copy")
	}
}

func TestVerifiedSnapshotPinsBytesAndRejectsTamperingSymlinksAndCancellation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "archive")
	path := filepath.Join(root, "1", "2", "sample.eml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := []byte("Subject: stable\r\n\r\noriginal")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	snapshot, size, err := SnapshotOriginal(t.Context(), root, path, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if size != int64(len(raw)) {
		t.Fatal("wrong snapshot size")
	}
	if _, err = os.Stat(snapshot.Name()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("snapshot must be unlinked")
	}
	if err = os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	actual, err := io.ReadAll(snapshot)
	if err != nil || !bytes.Equal(actual, raw) {
		t.Fatal("source mutation changed verified stream")
	}
	if _, _, err = SnapshotOriginal(t.Context(), root, path, digest); err == nil {
		t.Fatal("tampered original accepted")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "another")
	if err = os.WriteFile(target, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err = SnapshotOriginal(t.Context(), root, path, digest); err == nil {
		t.Fatal("symlink original accepted")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err = VerifyOriginal(ctx, root, path, digest); err == nil {
		t.Fatal("invalid path or cancellation accepted")
	}
}

func TestAttachmentSnapshotStreamsDecodedBytesBeyondResponseBuffer(t *testing.T) {
	raw, err := os.CreateTemp(t.TempDir(), "mime-*")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	header := "Content-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\nContent-Type: text/plain\r\n\r\nintro\r\n--x\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=large.bin\r\n\r\n"
	if _, err = io.WriteString(raw, header); err != nil {
		t.Fatal(err)
	}
	// The exact payload size intentionally exceeds the old 8 MiB response cap.
	payloadSize := int64(9 << 20)
	if _, err = io.Copy(raw, &repeatedReader{payloadSize}); err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(raw, "\r\n--x--\r\n"); err != nil {
		t.Fatal(err)
	}
	part, file, err := SnapshotAttachment(t.Context(), raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, file)
	if err != nil || size != payloadSize || part.Size != size || hex.EncodeToString(digest.Sum(nil)) != part.SHA256 {
		t.Fatalf("attachment changed/truncated: %+v %d %v", part, size, err)
	}
}

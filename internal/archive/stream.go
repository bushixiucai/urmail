package archive

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// contextReader bounds cancellation latency to one buffered filesystem read.
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func openOriginalFile(root, path string) (*os.File, error) {
	relative, err := archiveRelative(root, path)
	if err != nil {
		return nil, err
	}
	archive, err := openPrivateRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = archive.Close() }()
	parent := "."
	for _, part := range splitRelative(relative) {
		parent = filepath.Join(parent, part)
		info, e := archive.Lstat(parent)
		if e != nil {
			return nil, e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("archive original symlink refused")
		}
	}
	file, err := archive.Open(relative)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxMessage {
		_ = file.Close()
		return nil, errors.New("archive original not a bounded regular file")
	}
	return file, nil
}

func verifyFile(ctx context.Context, file *os.File, digest string) (int64, string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, "", err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(contextReader{ctx, file}, MaxMessage+1))
	actual := hex.EncodeToString(hash.Sum(nil))
	if err != nil {
		return 0, "", err
	}
	if size > MaxMessage || (digest != "" && (len(digest) != 64 || actual != digest)) {
		return 0, "", errors.New("archive original integrity unavailable or mismatch")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return 0, "", err
	}
	return size, actual, nil
}

// VerifyOriginal checks integrity without buffering the original in memory.
func VerifyOriginal(ctx context.Context, root, path, digest string) error {
	if len(digest) != 64 {
		return errors.New("archive original digest missing")
	}
	file, err := openOriginalFile(root, path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	_, _, err = verifyFile(ctx, file, digest)
	return err
}

// SnapshotOriginal verifies a private unlinked copy before returning any bytes.
// The returned descriptor binds validation and streaming even if the source is
// replaced or modified later. The caller owns Close on every outcome.
func SnapshotOriginal(ctx context.Context, root, path, digest string) (*os.File, int64, error) {
	if len(digest) != 64 {
		return nil, 0, errors.New("archive original digest missing")
	}
	source, err := openOriginalFile(root, path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = source.Close() }()
	snapshot, err := os.CreateTemp("", "urmail-archive-export-*")
	if err != nil {
		return nil, 0, err
	}
	if err = os.Remove(snapshot.Name()); err != nil {
		_ = snapshot.Close()
		return nil, 0, err
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(snapshot, hash), io.LimitReader(contextReader{ctx, source}, MaxMessage+1))
	if err == nil && (size > MaxMessage || hex.EncodeToString(hash.Sum(nil)) != digest) {
		err = errors.New("archive original integrity unavailable or mismatch")
	}
	if err == nil {
		_, err = snapshot.Seek(0, io.SeekStart)
	}
	if err != nil {
		_ = snapshot.Close()
		return nil, 0, err
	}
	return snapshot, size, nil
}

func writeRootStream(ctx context.Context, root *os.Root, name string, source io.ReadSeeker, size int64, digest string) error {
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(name), ".pending-"+hex.EncodeToString(random))
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(temporary) }()
	hash := sha256.New()
	copied, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(contextReader{ctx, source}, MaxMessage+1))
	if err == nil && (copied != size || copied > MaxMessage || hex.EncodeToString(hash.Sum(nil)) != digest) {
		err = errors.New("archive copy identity mismatch")
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	// Publish a complete inode without overwriting an existing canonical copy.
	return root.Link(temporary, name)
}

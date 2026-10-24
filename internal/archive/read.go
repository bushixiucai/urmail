package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ReadOriginal verifies the indexed original before releasing any bytes.
func ReadOriginal(root, path, digest string) ([]byte, error) {
	return readOriginal(root, path, digest, false)
}

func readOriginal(root, path, digest string, legacy bool) ([]byte, error) {
	relative, err := archiveRelative(root, path)
	if err != nil {
		return nil, err
	}
	archive, err := openPrivateRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = archive.Close() }()
	// Refuse symlinks at each component, even when they resolve inside the root.
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
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("archive original not regular")
	}
	raw, err := io.ReadAll(io.LimitReader(file, MaxMessage+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxMessage {
		return nil, errors.New("archive original limit")
	}
	hash := sha256.Sum256(raw)
	if !legacy && (len(digest) != 64 || hex.EncodeToString(hash[:]) != digest) {
		return nil, errors.New("archive original integrity unavailable or mismatch")
	}
	return raw, nil
}
func splitRelative(path string) []string {
	var parts []string
	for path != "." {
		dir, base := filepath.Split(path)
		if base != "" {
			parts = append([]string{base}, parts...)
		}
		path = filepath.Clean(dir)
		if path == "" {
			break
		}
	}
	return parts
}

// ValidateOwnershipPath binds content access to the indexed tenant. PostgreSQL
// row visibility alone must never turn a forged path into another tenant's bytes.
func ValidateOwnershipPath(root, path string, tenantID, mailboxID int64) error {
	relative, err := archiveRelative(root, path)
	if err != nil {
		return err
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if parts[0] != strconv.FormatInt(tenantID, 10) || (mailboxID > 0 && parts[1] != strconv.FormatInt(mailboxID, 10)) {
		return errors.New("archive path ownership mismatch")
	}
	return nil
}

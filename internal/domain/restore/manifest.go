// Package restore verifies file manifests before destructive overwrites.
// Package restore verifies file manifests before destructive overwrites.
package restore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// ErrIncomplete is the incomplete error.
var ErrIncomplete = errors.New("incomplete restore manifest")

// ErrDigestMismatch is the digest mismatch error.
var ErrDigestMismatch = errors.New("restore digest mismatch")

// File is one manifest entry expected to exist verbatim.
// File is one manifest entry expected to exist verbatim.
type File struct {
	Path   string
	SHA256 string
	Size   int64
}

// Verify implements the verify step.
func Verify(files []File, contents map[string][]byte) error {
	if len(files) == 0 || len(files) > 100000 {
		return ErrIncomplete
	}
	seen := map[string]struct{}{}
	for _, file := range files {
		if file.Path == "" || len(file.SHA256) != 64 || file.Size < 0 {
			return ErrIncomplete
		}
		if _, ok := seen[file.Path]; ok {
			return ErrIncomplete
		}
		seen[file.Path] = struct{}{}
		content, ok := contents[file.Path]
		if !ok || int64(len(content)) != file.Size {
			return ErrIncomplete
		}
		sum := sha256.Sum256(content)
		if hex.EncodeToString(sum[:]) != file.SHA256 {
			return ErrDigestMismatch
		}
	}
	if len(contents) != len(files) {
		return ErrIncomplete
	}
	return nil
}

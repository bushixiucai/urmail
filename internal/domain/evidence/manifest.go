// Package evidence implements domain rules for evidence.
package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
)

// ErrMismatch is the mismatch error.
var ErrMismatch = errors.New("evidence mismatch")

// Source implements the corresponding domain rule.
type Source struct {
	ID     string
	SHA256 string
	Size   int64
}

// Manifest implements the corresponding domain rule.
type Manifest struct {
	ApprovalDigest string
	Sources        []Source
	Digest         string
}

// Build implements the corresponding domain rule.
func Build(approvalDigest string, sources []Source) (Manifest, error) {
	if !validDigest(approvalDigest) || len(sources) == 0 || len(sources) > 1000 {
		return Manifest{}, ErrMismatch
	}
	copied := append([]Source(nil), sources...)
	sort.Slice(copied, func(i, j int) bool { return copied[i].ID < copied[j].ID })
	for i, source := range copied {
		if source.ID == "" || !validDigest(source.SHA256) || source.Size < 0 {
			return Manifest{}, ErrMismatch
		}
		if i > 0 && copied[i-1].ID == source.ID {
			return Manifest{}, ErrMismatch
		}
	}
	return Manifest{ApprovalDigest: approvalDigest, Sources: copied, Digest: digest(approvalDigest, copied)}, nil
}

// SourceFromOriginal implements the corresponding domain rule.
func SourceFromOriginal(id string, original []byte) Source {
	return Source{ID: id, SHA256: hash(original), Size: int64(len(original))}
}

// Verify implements the corresponding domain rule.
func Verify(manifest Manifest, files map[string][]byte) error {
	built, err := Build(manifest.ApprovalDigest, manifest.Sources)
	if err != nil || built.Digest != manifest.Digest {
		return ErrMismatch
	}
	if len(files) != len(manifest.Sources) {
		return ErrMismatch
	}
	for _, source := range manifest.Sources {
		file, ok := files[source.ID]
		if !ok || int64(len(file)) != source.Size || hash(file) != source.SHA256 {
			return ErrMismatch
		}
	}
	return nil
}

func hash(file []byte) string { sum := sha256.Sum256(file); return hex.EncodeToString(sum[:]) }
func digest(approval string, sources []Source) string {
	h := sha256.New()
	h.Write([]byte(approval))
	for _, source := range sources {
		h.Write([]byte(source.ID))
		h.Write([]byte(source.SHA256))
	}
	return hex.EncodeToString(h.Sum(nil))
}
func validDigest(v string) bool {
	if len(v) != 64 {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}

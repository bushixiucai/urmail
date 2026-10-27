package backupauto

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"filippo.io/age"
)

// VerifyOffsite streams every artifact of one manifest from the offsite
// destination and checks both ciphertext and plaintext integrity WITHOUT
// writing any local state (OPS-001 restore bar: restorable independently).
// Plaintext integrity proves the age envelope decrypts to exactly the bytes
// the manifest recorded at backup time.
func VerifyOffsite(ctx context.Context, store OffsiteStore, manifest *Manifest, identities []age.Identity) ([]VerifyResult, error) {
	results := make([]VerifyResult, 0, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		result := VerifyResult{Name: artifact.Name}
		cipherHash, err := checksumRemote(ctx, store, artifact.EncryptedName)
		if err != nil {
			result.Error = err
			results = append(results, result)
			continue
		}
		result.EncryptedOK = cipherHash == artifact.EncryptedSHA256
		if !result.EncryptedOK {
			result.Error = fmt.Errorf("ciphertext checksum mismatch: remote %s manifest %s", cipherHash, artifact.EncryptedSHA256)
			results = append(results, result)
			continue
		}
		plainHash, size, err := decryptChecksum(ctx, store, artifact, identities)
		if err != nil {
			result.Error = fmt.Errorf("decrypt check: %w", err)
			results = append(results, result)
			continue
		}
		result.PlaintextOK = plainHash == artifact.SHA256 && size == artifact.Size
		if !result.PlaintextOK {
			result.Error = fmt.Errorf("plaintext checksum/size mismatch: remote %s (%d bytes) manifest %s (%d bytes)", plainHash, size, artifact.SHA256, artifact.Size)
		}
		results = append(results, result)
	}
	return results, nil
}

// VerifyResult reports one artifact's remote integrity.
type VerifyResult struct {
	Name        string
	EncryptedOK bool
	PlaintextOK bool
	Error       error
}

// OK reports full integrity.
func (r VerifyResult) OK() bool { return r.Error == nil && r.EncryptedOK && r.PlaintextOK }

func checksumRemote(ctx context.Context, store OffsiteStore, name string) (string, error) {
	pr, pw := io.Pipe()
	hashDone := make(chan string, 1)
	go func() {
		defer func() { _ = pr.Close() }()
		h := sha256.New()
		_, err := io.Copy(h, pr)
		if err != nil {
			hashDone <- ""
			return
		}
		hashDone <- hex.EncodeToString(h.Sum(nil))
	}()
	err := store.Download(ctx, name, pw)
	_ = pw.CloseWithError(err)
	if err != nil {
		return "", err
	}
	return <-hashDone, nil
}

// LocalArtifact is one plaintext artifact produced by the backup step.
type LocalArtifact struct {
	Path   string
	Name   string
	Kind   string
	Size   int64
	SHA256 string
}

// EncryptAndUpload encrypts one plaintext artifact with the age recipient
// and streams the ciphertext to the offsite destination without ever
// materializing a second plaintext copy. It returns the v2 manifest entry
// (plaintext + ciphertext hashes).
func EncryptAndUpload(ctx context.Context, store OffsiteStore, artifact LocalArtifact, recipient string) (Artifact, error) {
	parsed, err := ParseRecipient(recipient)
	if err != nil {
		return Artifact{}, err
	}
	f, err := os.Open(artifact.Path) // #nosec G304 -- caller-built artifact path inside the private spool.
	if err != nil {
		return Artifact{}, fmt.Errorf("open artifact %s: %w", artifact.Name, err)
	}
	defer func() { _ = f.Close() }()

	pr, pw := io.Pipe()
	digest := sha256.New()
	encryptErr := make(chan error, 1)
	go func() { // plaintext file -> age -> pipe; exactly one pipe consumer below
		err := EncryptTo(pw, f, parsed)
		_ = pw.CloseWithError(err)
		encryptErr <- err
	}()

	// The upload consumes the ciphertext once; TeeReader mirrors every byte
	// into the digest so the manifest records the hash of the exact bytes
	// that reached the destination.
	uploadErr := store.Upload(ctx, EncryptedName(artifact.Name), io.TeeReader(pr, digest), -1)
	encErr := <-encryptErr
	if uploadErr != nil {
		return Artifact{}, fmt.Errorf("upload %s: %w", artifact.Name, uploadErr)
	}
	if encErr != nil {
		return Artifact{}, fmt.Errorf("encrypt %s: %w", artifact.Name, encErr)
	}
	size := int64(digest.Size())
	if size < 0 {
		size = 0
	}
	return Artifact{
		Name:            artifact.Name,
		SHA256:          artifact.SHA256,
		Size:            artifact.Size,
		EncryptedName:   EncryptedName(artifact.Name),
		EncryptedSHA256: hex.EncodeToString(digest.Sum(nil)),
		EncryptedSize:   size,
		Algorithm:       EncryptionAlgorithmAge,
		Kind:            artifact.Kind,
	}, nil
}

// decryptChecksum streams one remote ciphertext into the age decryptor and
// returns the plaintext SHA-256 and size. Nothing is written to disk.
func decryptChecksum(ctx context.Context, store OffsiteStore, artifact Artifact, identities []age.Identity) (string, int64, error) {
	if len(identities) == 0 {
		return "", 0, fmt.Errorf("decryption requires an age identity")
	}
	pr, pw := io.Pipe()
	digest := sha256.New()
	counter := &countingWriter{inner: digest}
	decryptErr := make(chan error, 1)
	go func() {
		err := DecryptFrom(counter, pr, identities...)
		_ = pr.CloseWithError(err)
		decryptErr <- err
	}()
	err := store.Download(ctx, artifact.EncryptedName, pw)
	if err != nil {
		_ = pw.CloseWithError(err)
		if derr := <-decryptErr; derr != nil {
			return "", 0, fmt.Errorf("download %s: %w (decryptor: %w)", artifact.EncryptedName, err, derr)
		}
		return "", 0, fmt.Errorf("download %s: %w", artifact.EncryptedName, err)
	}
	_ = pw.Close()
	if err := <-decryptErr; err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(digest.Sum(nil)), counter.n, nil
}

// countingWriter counts plaintext bytes while hashing.
type countingWriter struct {
	inner io.Writer
	n     int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.inner.Write(p)
	w.n += int64(n)
	return n, err
}

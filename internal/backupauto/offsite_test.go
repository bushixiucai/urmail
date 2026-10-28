package backupauto

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
)

func TestSignV4DeterministicAndComplete(t *testing.T) {
	when := time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)
	a := signV4("GET", "bucket.s3.us-east-1.amazonaws.com", "/", "list-type=2&prefix=x", "us-east-1", "s3", "AKID", "SECRET", emptyPayloadHash, when)
	b := signV4("GET", "bucket.s3.us-east-1.amazonaws.com", "/", "prefix=x&list-type=2", "us-east-1", "s3", "AKID", "SECRET", emptyPayloadHash, when)
	if a.Get("Authorization") != b.Get("Authorization") {
		t.Fatal("query ordering must not change the signature")
	}
	if a.Get("x-amz-date") != "20261027T080000Z" {
		t.Fatalf("unexpected amz date %q", a.Get("x-amz-date"))
	}
	for _, header := range []string{"Authorization", "x-amz-date", "x-amz-content-sha256"} {
		if a.Get(header) == "" {
			t.Fatalf("missing %s", header)
		}
	}
	if !strings.Contains(a.Get("Authorization"), "Credential=AKID/20261027/us-east-1/s3/aws4_request") {
		t.Fatalf("unexpected credential scope: %s", a.Get("Authorization"))
	}
}

func TestSortQuery(t *testing.T) {
	if sortQuery("") != "" || sortQuery("b=2&a=1") != "a=1&b=2" {
		t.Fatal("query must be sorted for signing")
	}
}

// memoryStore is a deterministic in-memory OffsiteStore for pipeline tests.
type memoryStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMemoryStore() *memoryStore { return &memoryStore{objects: map[string][]byte{}} }

func (m *memoryStore) Upload(_ context.Context, name string, r io.Reader, _ int64) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[name] = data
	return nil
}

func (m *memoryStore) List(_ context.Context) ([]RemoteObject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RemoteObject, 0, len(m.objects))
	for name, data := range m.objects {
		out = append(out, RemoteObject{Name: name, Size: int64(len(data)), LastModified: time.Now()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *memoryStore) Download(_ context.Context, name string, w io.Writer) error {
	m.mu.Lock()
	data, ok := m.objects[name]
	m.mu.Unlock()
	if !ok {
		return os.ErrNotExist
	}
	_, err := w.Write(data)
	return err
}

func (m *memoryStore) Delete(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, name)
	return nil
}

func (m *memoryStore) tamper(t *testing.T, name string) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[name][0] ^= 0xff
}

func TestEncryptUploadVerifyRoundTrip(t *testing.T) {
	ctx := context.Background()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	pub := identity.Recipient().String()
	store := newMemoryStore()

	plaintextFile := filepath.Join(t.TempDir(), "urmail-x.dump")
	plaintext := bytes.Repeat([]byte("mail data\x00"), 500)
	if err := os.WriteFile(plaintextFile, plaintext, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(plaintext)
	artifact := LocalArtifact{Name: "urmail-x.dump", Path: plaintextFile, Kind: KindPgDump, Size: int64(len(plaintext)), SHA256: hex.EncodeToString(sum[:])}

	entry, err := EncryptAndUpload(ctx, store, artifact, pub)
	if err != nil {
		t.Fatalf("encrypt+upload: %v", err)
	}
	if entry.EncryptedSHA256 == "" || entry.EncryptedSize <= 32 || entry.EncryptedSize != int64(len(store.objects[entry.EncryptedName])) {
		t.Fatalf("ciphertext metadata missing: %+v", entry)
	}

	remote, err := checksumRemote(ctx, store, entry.EncryptedName)
	if err != nil || remote != entry.EncryptedSHA256 {
		t.Fatalf("ciphertext verify failed: %q %v", remote, err)
	}

	manifest := &Manifest{Version: ManifestVersion2, Artifacts: []Artifact{entry}, Encryption: &EncryptionInfo{Algorithm: EncryptionAlgorithmAge, Recipient: pub}}
	results, err := VerifyOffsite(ctx, store, manifest, []age.Identity{identity})
	if err != nil {
		t.Fatalf("verify-offsite: %v", err)
	}
	if len(results) != 1 || !results[0].OK() {
		t.Fatalf("verify results not OK: %+v", results)
	}

	store.tamper(t, entry.EncryptedName)
	results, err = VerifyOffsite(ctx, store, manifest, []age.Identity{identity})
	if err != nil {
		t.Fatalf("verify-offsite run: %v", err)
	}
	if len(results) != 1 || results[0].OK() {
		t.Fatalf("tampered object must fail verification: %+v", results)
	}
}

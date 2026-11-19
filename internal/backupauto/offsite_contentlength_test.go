package backupauto

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"filippo.io/age"
)

// putRecord captures the wire facts of one S3 PUT: the exact Content-Length
// the server saw and whether the body arrived chunked instead.
type putRecord struct {
	mu               sync.Mutex
	contentLength    int64
	transferEncoding string
	body             []byte
}

func (p *putRecord) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.contentLength = r.ContentLength
		if len(r.TransferEncoding) > 0 {
			p.transferEncoding = r.TransferEncoding[0]
		}
		p.body = body
		p.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
}

// newRecordingS3Store wires an s3Store at a recording test endpoint.
func newRecordingS3Store(t *testing.T) (*s3Store, *putRecord) {
	t.Helper()
	rec := &putRecord{}
	server := httptest.NewServer(rec.handler())
	t.Cleanup(server.Close)
	store, err := NewS3Store("vw-backup", "prefix", S3Credentials{
		AccessKeyID: "AKIDEXAMPLE", SecretKey: "secret", Region: "eu-het-01", Endpoint: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	s3, ok := store.(*s3Store)
	if !ok {
		t.Fatalf("NewS3Store returned %T", store)
	}
	return s3, rec
}

// TestS3UploadRefusesUnknownLength proves the 411 defense in depth: an
// unknown-length (-1) upload must be refused client-side instead of ever
// reaching the wire as a chunked PUT, which MinIO answers with
// "HTTP 411 MissingContentLength".
func TestS3UploadRefusesUnknownLength(t *testing.T) {
	store, rec := newRecordingS3Store(t)
	err := store.Upload(t.Context(), "urmail-x.dump.age", bytes.NewReader([]byte("ciphertext bytes")), -1)
	if err == nil {
		t.Fatal("unknown-length upload must be refused")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("411")) {
		t.Fatalf("refusal must name the 411 hazard, got %q", err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.body != nil {
		t.Fatal("refused upload must not reach the wire")
	}
}

// TestS3UploadSendsExactContentLength is the happy-path wire contract: a
// PUT with a known size carries that exact Content-Length and never chunked
// transfer-encoding.
func TestS3UploadSendsExactContentLength(t *testing.T) {
	store, rec := newRecordingS3Store(t)
	payload := bytes.Repeat([]byte("age ciphertext block"), 64)
	if err := store.Upload(t.Context(), "urmail-x.dump.age", bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("upload: %v", err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.contentLength != int64(len(payload)) {
		t.Fatalf("Content-Length = %d, want %d", rec.contentLength, len(payload))
	}
	if rec.transferEncoding != "" {
		t.Fatalf("PUT must not be chunked, got %q", rec.transferEncoding)
	}
	if !bytes.Equal(rec.body, payload) {
		t.Fatal("uploaded bytes differ from the spooled ciphertext")
	}
}

// TestEncryptAndUploadSpoolsCiphertextForContentLength proves the 411 root
// cause fix end to end: EncryptAndUpload (which internally cannot know the
// ciphertext length before encrypting) must spool to a private file and PUT
// with the exact Content-Length, and the spool file must not be left behind.
func TestEncryptAndUploadSpoolsCiphertextForContentLength(t *testing.T) {
	store, rec := newRecordingS3Store(t)
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	plaintext := bytes.Repeat([]byte("mail data\x00"), 500)
	plaintextFile := filepath.Join(dir, "urmail-x.dump")
	if err := os.WriteFile(plaintextFile, plaintext, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(plaintext)
	artifact := LocalArtifact{
		Name: "urmail-x.dump", Path: plaintextFile, Kind: KindPgDump,
		Size: int64(len(plaintext)), SHA256: hex.EncodeToString(sum[:]),
	}

	entry, err := EncryptAndUpload(t.Context(), store, artifact, identity.Recipient().String())
	if err != nil {
		t.Fatalf("encrypt+upload: %v", err)
	}
	rec.mu.Lock()
	if rec.contentLength <= 0 || rec.contentLength != entry.EncryptedSize {
		rec.mu.Unlock()
		t.Fatalf("ciphertext PUT must carry the exact spooled length: Content-Length=%d entry.EncryptedSize=%d", rec.contentLength, entry.EncryptedSize)
	}
	if rec.transferEncoding != "" {
		rec.mu.Unlock()
		t.Fatalf("ciphertext PUT must not be chunked, got %q", rec.transferEncoding)
	}
	if int64(len(rec.body)) != rec.contentLength {
		rec.mu.Unlock()
		t.Fatalf("server read %d bytes against a %d Content-Length: the digest must cover exactly the stat'd spool size", len(rec.body), rec.contentLength)
	}
	body := rec.body
	rec.mu.Unlock()
	if got := sha256.Sum256(body); hex.EncodeToString(got[:]) != entry.EncryptedSHA256 {
		t.Fatal("uploaded bytes must hash to the recorded ciphertext hash")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "urmail-x.dump" {
		t.Fatalf("spool file must be removed after upload; dir has %d entries", len(entries))
	}
}

// TestCreateCipherSpoolIsPrivate pins the spool hygiene (A3): the ciphertext
// temp file is created 0600 inside the artifact's own directory.
func TestCreateCipherSpoolIsPrivate(t *testing.T) {
	dir := t.TempDir()
	f, err := createCipherSpool(filepath.Join(dir, "urmail-x.dump"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(f.Name()) != dir {
		t.Fatalf("cipher spool must live beside the plaintext, got %s", f.Name())
	}
	info, err := os.Stat(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cipher spool must be private (0600), got %v", info.Mode().Perm())
	}
}

// TestCipherSpoolSpacePrecheck pins the A1 guard: a normal directory passes,
// and on Linux a spool larger than the visible free space is refused before
// any ciphertext is written.
func TestCipherSpoolSpacePrecheck(t *testing.T) {
	dir := t.TempDir()
	if err := requireCipherSpoolSpace(filepath.Join(dir, "urmail-x.dump"), 1024); err != nil {
		t.Fatalf("normal spool must pass the precheck: %v", err)
	}
	if runtime.GOOS != "linux" {
		t.Skip("free-space readout is a Linux statfs contract; other platforms skip the precheck")
	}
	if err := requireCipherSpoolSpace(filepath.Join(dir, "urmail-x.dump"), 1<<50); err == nil {
		t.Fatal("artifact larger than the visible free space must fail the precheck")
	}
}

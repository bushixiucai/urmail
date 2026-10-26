package archive

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
)

// Fixed storage roots are managed data, never caller-provided download paths.
const (
	SpoolRoot   = "/var/lib/urmail/archive-spool"
	ArchiveRoot = "/var/lib/urmail/archive"
	// MaxMessage covers the v80 SMTP contract plus bounded internal headers.
	MaxMessage = (128 << 20) + (1 << 20)
	// MaxExtraction keeps MIME indexing independent from durable raw retention.
	MaxExtraction = 64 << 20
)

// Manifest survives database/config outages and pins every delayed copy's identity.
type Manifest struct {
	Version    int                         `json:"version"`
	ID         string                      `json:"id"`
	Recipient  domainMail.ArchiveRecipient `json:"recipient"`
	SHA256     string                      `json:"sha256"`
	Size       int64                       `json:"size"`
	ReceivedAt time.Time                   `json:"received_at"`
}

// Spool writes original bytes before attempting any configuration or database access.
func Spool(root string, recipient domainMail.ArchiveRecipient, reader io.Reader) (manifest Manifest, resultErr error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return manifest, err
	}
	id := hex.EncodeToString(random)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return manifest, err
	}
	dir, err := openPrivateRoot(root)
	if err != nil {
		return manifest, err
	}
	defer func() { _ = dir.Close() }()
	pending := ".pending-" + id
	if err = dir.Mkdir(pending, 0o700); err != nil {
		return manifest, err
	}
	published := false
	defer func() {
		if !published {
			_ = dir.RemoveAll(pending)
		}
	}()
	file, err := dir.OpenFile(pending+"/raw.eml", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return manifest, err
	}
	digest := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(file, digest), io.LimitReader(reader, MaxMessage+1))
	if copyErr == nil && size > MaxMessage {
		copyErr = errors.New("archive message exceeds limit")
	}
	if copyErr == nil {
		copyErr = file.Sync()
	}
	closeErr := file.Close()
	if copyErr != nil {
		return manifest, copyErr
	}
	if closeErr != nil {
		return manifest, closeErr
	}
	manifest = Manifest{Version: 1, ID: id, Recipient: recipient, SHA256: hex.EncodeToString(digest.Sum(nil)), Size: size, ReceivedAt: time.Now().UTC()}
	encoded, _ := json.Marshal(manifest)
	if err = writeRootFile(dir, pending+"/manifest.json", encoded); err != nil {
		return manifest, err
	}
	if err = syncDirectory(filepath.Join(root, pending)); err != nil {
		return manifest, err
	}
	if err = dir.Rename(pending, id); err != nil {
		return manifest, err
	}
	published = true
	return manifest, syncDirectory(root)
}

func writeRootFile(root *os.Root, name string, raw []byte) error {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func syncDirectory(path string) error {
	dir, err := os.Open(path) // #nosec G304 -- private storage path generated from fixed root and random lowercase hex IDs.
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

// BindLegacy records a canonical mailbox resolved during the original pipe call.
// Delayed workers must never guess a legacy recipient's ownership after reuse.
func BindLegacy(root string, manifest Manifest, tenantID, mailboxID int64) error {
	if !validID(manifest.ID) || tenantID <= 0 || mailboxID <= 0 {
		return errors.New("invalid archive binding")
	}
	dir, err := openPrivateRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	manifest.Recipient.TenantID = tenantID
	manifest.Recipient.MailboxID = mailboxID
	encoded, _ := json.Marshal(manifest)
	if err = writeRootFile(dir, manifest.ID+"/bound.json", encoded); err != nil {
		return err
	}
	return syncDirectory(filepath.Join(root, manifest.ID))
}
func validID(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func openManifest(root *os.Root, id string) (Manifest, *os.File, error) {
	var manifest Manifest
	if !validID(id) {
		return manifest, nil, errors.New("invalid spool id")
	}
	encoded, err := root.ReadFile(id + "/manifest.json")
	if err != nil {
		return manifest, nil, err
	}
	if bound, e := root.ReadFile(id + "/bound.json"); e == nil {
		encoded = bound
	} else if !errors.Is(e, os.ErrNotExist) {
		return manifest, nil, e
	}
	if len(encoded) > 4096 {
		return manifest, nil, errors.New("oversized archive manifest")
	}
	if err = json.Unmarshal(encoded, &manifest); err != nil {
		return manifest, nil, err
	}
	if (manifest.Version != 0 && manifest.Version != 1) || manifest.ID != id || manifest.Size < 0 || manifest.Size > MaxMessage || manifest.ReceivedAt.IsZero() {
		return manifest, nil, errors.New("invalid archive manifest identity")
	}
	file, err := root.Open(id + "/raw.eml")
	if err != nil {
		return manifest, nil, err
	}
	digest := sha256.New()
	size, err := io.Copy(digest, io.LimitReader(file, MaxMessage+1))
	if err != nil {
		_ = file.Close()
		return manifest, nil, err
	}
	if size != manifest.Size || hex.EncodeToString(digest.Sum(nil)) != manifest.SHA256 {
		_ = file.Close()
		return manifest, nil, fmt.Errorf("archive original hash mismatch")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return manifest, nil, err
	}
	return manifest, file, nil
}

// readManifest remains a bounded compatibility helper for small local callers.
func readManifest(root *os.Root, id string) (Manifest, []byte, error) {
	manifest, file, err := openManifest(root, id)
	if err != nil {
		return manifest, nil, err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, MaxMessage+1))
	return manifest, raw, err
}

func openPrivateRoot(path string) (*os.Root, error) {
	info, err := os.Lstat(path) // #nosec G703 -- private fixed storage root or isolated test root; never a tenant argument.
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("archive storage root must be a private real directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(stat.Uid) != int64(os.Geteuid()) {
		return nil, errors.New("archive storage root ownership mismatch")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	current, err := root.Stat(".")
	if err != nil || !os.SameFile(info, current) {
		_ = root.Close()
		return nil, errors.New("archive storage root changed during open")
	}
	return root, nil
}

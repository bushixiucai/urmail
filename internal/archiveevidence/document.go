package archiveevidence

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// MaxSources bounds one independently approved source set.
const MaxSources = 100

// MaxPackageBytes bounds total original bytes before ZIP overhead.
const MaxPackageBytes int64 = 512 << 20

// MaxSourceBytes matches the archive original size limit.
const MaxSourceBytes int64 = 128 << 20

// File binds a canonical ZIP member to the exact archived RFC822 original.
type File struct {
	EntryID int64  `json:"entry_id"`
	Name    string `json:"name"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// Document is an immutable custody/approval snapshot, never an authorization token.
type Document struct {
	Version      int    `json:"version"`
	TenantID     int64  `json:"tenant_id"`
	CaseID       int64  `json:"case_id"`
	CaseVersion  int64  `json:"case_version"`
	Title        string `json:"title"`
	Purpose      string `json:"purpose"`
	ApprovalID   int64  `json:"approval_id"`
	SourceDigest string `json:"source_digest"`
	RequesterID  int64  `json:"requester_id"`
	ApproverID   int64  `json:"approver_id"`
	ExporterID   int64  `json:"exporter_id"`
	ApprovedAt   string `json:"approved_at"`
	GeneratedAt  string `json:"generated_at"`
	Files        []File `json:"files"`
}

var sourceName = regexp.MustCompile(`^messages/[1-9][0-9]*\.eml$`)

// EncodeDocument rejects ambiguous paths, repeated identities and unbounded sets.
func EncodeDocument(doc Document) ([]byte, error) {
	if doc.Version != 1 || doc.CaseID <= 0 || doc.TenantID <= 0 || doc.ApprovalID <= 0 || doc.CaseVersion <= 0 || doc.RequesterID <= 0 || doc.ApproverID <= 0 || doc.RequesterID == doc.ApproverID || doc.ExporterID != doc.RequesterID || !validSHA(doc.SourceDigest) || len(doc.Files) == 0 || len(doc.Files) > MaxSources {
		return nil, errors.New("invalid evidence document")
	}
	approved, approvedErr := time.Parse(time.RFC3339Nano, doc.ApprovedAt)
	generated, generatedErr := time.Parse(time.RFC3339Nano, doc.GeneratedAt)
	if len(strings.TrimSpace(doc.Title)) == 0 || len(doc.Title) > 200 || len(strings.TrimSpace(doc.Purpose)) == 0 || len(doc.Purpose) > 2000 || approvedErr != nil || generatedErr != nil || approved.IsZero() || generated.IsZero() {
		return nil, errors.New("invalid custody metadata")
	}
	seen := map[int64]bool{}
	var total int64
	for _, f := range doc.Files {
		if f.EntryID <= 0 || seen[f.EntryID] || !sourceName.MatchString(f.Name) || f.Name != fmt.Sprintf("messages/%d.eml", f.EntryID) || !validSHA(f.SHA256) || f.Size < 0 || f.Size > MaxSourceBytes || f.Size > MaxPackageBytes-total {
			return nil, errors.New("invalid evidence source")
		}
		seen[f.EntryID] = true
		total += f.Size
	}
	return json.MarshalIndent(doc, "", "  ")
}
func validSHA(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

// VerifyPackage anchors the manifest to the independently obtained authenticated
// SHA256, then streams every original. It rejects missing, extra, duplicate,
// traversing and oversized members, including ZIP bombs.
func VerifyPackage(source io.ReaderAt, size int64, expectedManifest string) (Document, error) {
	var doc Document
	if !validSHA(expectedManifest) || size < 0 || size > MaxPackageBytes+(4<<20) {
		return doc, errors.New("invalid package/expected manifest SHA256")
	}
	reader, err := zip.NewReader(source, size)
	if err != nil {
		return doc, err
	}
	files := map[string]*zip.File{}
	for _, f := range reader.File {
		if _, ok := files[f.Name]; ok {
			return doc, errors.New("duplicate ZIP member")
		}
		files[f.Name] = f
	}
	manifest, ok := files["manifest.json"]
	if !ok || manifest.UncompressedSize64 > 1<<20 {
		return doc, errors.New("missing/oversized evidence manifest")
	}
	r, err := manifest.Open()
	if err != nil {
		return doc, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(r, 1<<20+1))
	closeErr := r.Close()
	if err = errors.Join(readErr, closeErr); err != nil {
		return doc, err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != expectedManifest {
		return doc, errors.New("manifest SHA256 mismatch")
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		return doc, err
	}
	if _, err = EncodeDocument(doc); err != nil {
		return doc, err
	}
	if len(files) != len(doc.Files)+2 {
		return doc, errors.New("extra/missing ZIP member")
	}
	guide, ok := files["VERIFYING.txt"]
	if !ok || guide.UncompressedSize64 > 4096 {
		return doc, errors.New("missing verification guide")
	}
	guideReader, err := guide.Open()
	if err != nil {
		return doc, err
	}
	guideBody, guideReadErr := io.ReadAll(io.LimitReader(guideReader, 4097))
	guideCloseErr := guideReader.Close()
	if err = errors.Join(guideReadErr, guideCloseErr); err != nil {
		return doc, err
	}
	if string(guideBody) != VerificationGuide {
		return doc, errors.New("verification guide changed")
	}
	for _, f := range doc.Files {
		entry, ok := files[f.Name]
		if !ok || !entry.Mode().IsRegular() || entry.UncompressedSize64 > 128<<20 {
			return doc, errors.New("missing/oversized original")
		}
		if int64(entry.UncompressedSize64) != f.Size {
			return doc, errors.New("missing/mis-sized original")
		}
		r, openErr := entry.Open()
		if openErr != nil {
			return doc, openErr
		}
		hash := sha256.New()
		count, copyErr := io.Copy(hash, io.LimitReader(r, f.Size+1))
		closeErr := r.Close()
		if err = errors.Join(copyErr, closeErr); err != nil {
			return doc, err
		}
		if count != f.Size || hex.EncodeToString(hash.Sum(nil)) != f.SHA256 {
			return doc, errors.New("original SHA256/size mismatch")
		}
	}
	return doc, nil
}

// VerificationGuide directs recipients to a separately trusted verifier.
const VerificationGuide = "Verify this package with an independently installed trusted urmailctl:\n\nurmailctl evidence verify --manifest-sha256 EXPECTED_SHA256 package.zip\n\nCopy EXPECTED_SHA256 from the authenticated investigation screen or custody record.\nThis verifies exact approved original bytes, file sizes, membership and manifest integrity.\nIt does not certify legal admissibility or a third-party timestamp.\n"

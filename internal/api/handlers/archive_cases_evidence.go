package handlers

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	archivestore "github.com/bushixiucai/urmail/internal/archive"
	"github.com/bushixiucai/urmail/internal/archiveevidence"
	"github.com/bushixiucai/urmail/internal/store"
)

func (h *archiveCases) prepareEvidence(c *gin.Context) {
	q, t, u, ok := h.principal(c, "export")
	if !ok {
		return
	}
	row, ok := h.caseRow(c, q, t, true)
	if !ok {
		return
	}
	id, ok := casePathID(c, "approval_id")
	if !ok {
		return
	}
	var input struct {
		Digest  string `json:"digest"`
		Confirm bool   `json:"confirm"`
	}
	if c.ShouldBindJSON(&input) != nil || !input.Confirm {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	approved, err := q.ArchiveCasesApproval(c, store.ArchiveCasesApprovalParams{TenantID: t, InvestigationID: row.ID, ID: id})
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	if approved.RequesterUserID != u {
		authError(c, 403, "FORBIDDEN")
		return
	}
	visible, err := q.ArchiveCasesExportVisible(c, store.ArchiveCasesExportVisibleParams{T: t, A: id, U: u})
	if err != nil || !visible || input.Digest != approved.SourceDigest {
		authError(c, 409, "ARCHIVE_CASE_STALE")
		return
	}
	existing, err := q.ArchiveCasesGetEvidence(c, store.ArchiveCasesGetEvidenceParams{TenantID: t, ApprovalID: id})
	if err == nil {
		c.JSON(200, gin.H{"manifest_sha256": existing.ManifestSha256, "approval_id": id})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		archiveCaseError(c, err)
		return
	}
	sources, err := q.ArchiveCasesSources(c, store.ArchiveCasesSourcesParams{TenantID: t, InvestigationID: row.ID})
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	document := archiveevidence.Document{Version: 1, TenantID: t, CaseID: row.ID, CaseVersion: row.Version, Title: row.Title, Purpose: row.Purpose, ApprovalID: id, SourceDigest: approved.SourceDigest, RequesterID: approved.RequesterUserID, ApproverID: approved.ApproverUserID.Int64, ExporterID: u, ApprovedAt: approved.DecidedAt.Time.UTC().Format(time.RFC3339Nano), GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	for _, s := range sources {
		if s.DeletedAt.Valid {
			authError(c, 409, "ARCHIVE_CASE_STALE")
			return
		}
		document.Files = append(document.Files, archiveevidence.File{EntryID: s.ID, Name: fmt.Sprintf("messages/%d.eml", s.ID), SHA256: s.RawSha256, Size: s.SizeBytes})
	}
	body, err := archiveevidence.EncodeDocument(document)
	if err != nil {
		authError(c, 400, "ARCHIVE_PACKAGE_LIMIT")
		return
	}
	hash := sha256.Sum256(body)
	digest := hex.EncodeToString(hash[:])
	_, err = q.ArchiveCasesSaveEvidence(c, store.ArchiveCasesSaveEvidenceParams{TenantID: t, ApprovalID: id, CreatedBy: u, Manifest: body, ManifestSha256: digest})
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	if !caseAudit(c, q, t, u, "PREPARE archive.evidence_manifest", id) {
		return
	}
	c.JSON(201, gin.H{"manifest_sha256": digest, "approval_id": id})
}

// currentEvidencePermission uses a fresh statement, outside the request's
// transaction, on every bounded block. No case or grant lock is held while a
// package streams, so a revoke/close/role change can take effect immediately.
func (h *archiveCases) currentEvidencePermission(ctx context.Context, t, request, u int64) error {
	tx, err := h.App.UserTx(ctx, t, u)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	allowed, err := store.New(tx).ArchiveCasesExportVisible(ctx, store.ArchiveCasesExportVisibleParams{T: t, A: request, U: u})
	if err != nil {
		return err
	}
	if !allowed {
		return errors.New("evidence access revoked")
	}
	return nil
}

type evidenceGuardedReader struct {
	ctx    context.Context
	source io.Reader
	check  func(context.Context) error
}

func (r evidenceGuardedReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if err := r.check(r.ctx); err != nil {
		return 0, err
	}
	if len(p) > 32<<10 {
		p = p[:32<<10]
	}
	return r.source.Read(p)
}

type evidenceDownload struct {
	source     *os.File
	guard      evidenceGuardedReader
	size, read int64
	failed     bool
	once       sync.Once
	release    func()
	complete   func(bool, int64)
}

func (r *evidenceDownload) Read(p []byte) (int, error) {
	n, err := r.guard.Read(p)
	r.read += int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		r.failed = true
	}
	return n, err
}
func (r *evidenceDownload) Close() error {
	var err error
	r.once.Do(func() { err = r.source.Close(); r.release(); r.complete(!r.failed && r.read == r.size, r.read) })
	return err
}

func (h *archiveCases) downloadPackage(c *gin.Context) {
	q, t, u, ok := h.principal(c, "export")
	if !ok {
		return
	}
	row, ok := h.caseRow(c, q, t, false)
	if !ok {
		return
	}
	id, ok := casePathID(c, "approval_id")
	if !ok {
		return
	}
	evidence, err := q.ArchiveCasesGetEvidence(c, store.ArchiveCasesGetEvidenceParams{TenantID: t, ApprovalID: id})
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	// Reject copying a valid approval id onto another case URL.
	var doc archiveevidence.Document
	if json.Unmarshal(evidence.Manifest, &doc) != nil || doc.CaseID != row.ID || doc.ApprovalID != id || doc.TenantID != t {
		authError(c, 403, "FORBIDDEN")
		return
	}
	if _, err = archiveevidence.EncodeDocument(doc); err != nil {
		authError(c, 503, "ARCHIVE_PACKAGE_INVALID")
		return
	}
	check := func(ctx context.Context) error { return h.currentEvidencePermission(ctx, t, id, u) }
	if err = check(c.Request.Context()); err != nil {
		authError(c, 403, "FORBIDDEN")
		return
	}
	if c.Request.Method == http.MethodHead {
		if !caseAudit(c, q, t, u, "HEAD archive.evidence_package", id) {
			return
		}
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=evidence-case-%d-approval-%d.zip", row.ID, id))
		c.Header("Content-Type", "application/zip")
		c.Header("X-Manifest-SHA256", evidence.ManifestSha256)
		c.Header("Cache-Control", "no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Accept-Ranges", "none")
		c.Status(http.StatusOK)
		return
	}
	release, ok := acquireArchiveTransfer(c)
	if !ok {
		return
	}
	owned := false
	defer func() {
		if !owned {
			release()
		}
	}()
	sources, err := q.ArchiveCasesSources(c, store.ArchiveCasesSourcesParams{TenantID: t, InvestigationID: row.ID})
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	root := h.Root
	if root == "" {
		root = archivestore.ArchiveRoot
	}
	packageFile, err := os.CreateTemp("", "urmail-evidence-*")
	if err != nil {
		authError(c, 503, "ARCHIVE_UNAVAILABLE")
		return
	}
	defer func() {
		if !owned {
			_ = packageFile.Close()
		}
	}()
	if err = os.Remove(packageFile.Name()); err != nil {
		authError(c, 503, "ARCHIVE_UNAVAILABLE")
		return
	}
	writer := zip.NewWriter(packageFile)
	appendFile := func(name string, source io.Reader) error {
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		header.SetMode(0o600)
		header.Modified = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
		destination, e := writer.CreateHeader(header)
		if e != nil {
			return e
		}
		_, e = io.Copy(destination, evidenceGuardedReader{ctx: c.Request.Context(), source: source, check: check})
		return e
	}
	if err = appendFile("manifest.json", bytes.NewReader(evidence.Manifest)); err != nil {
		_ = writer.Close()
		authError(c, 403, "FORBIDDEN")
		return
	}
	if err = appendFile("VERIFYING.txt", strings.NewReader(archiveevidence.VerificationGuide)); err != nil {
		_ = writer.Close()
		authError(c, 403, "FORBIDDEN")
		return
	}
	if len(sources) != len(doc.Files) {
		_ = writer.Close()
		authError(c, 409, "ARCHIVE_CASE_STALE")
		return
	}
	for i, s := range sources {
		expected := doc.Files[i]
		if expected.EntryID != s.ID || expected.SHA256 != s.RawSha256 || expected.Size != s.SizeBytes || s.DeletedAt.Valid || !s.MailboxID.Valid {
			_ = writer.Close()
			authError(c, 409, "ARCHIVE_CASE_STALE")
			return
		}
		if err = archivestore.ValidateOwnershipPath(root, s.Path, t, s.MailboxID.Int64); err != nil {
			_ = writer.Close()
			authError(c, 503, "ARCHIVE_PACKAGE_INVALID")
			return
		}
		snapshot, size, openErr := archivestore.SnapshotOriginal(c.Request.Context(), root, s.Path, s.RawSha256)
		if openErr != nil {
			_ = writer.Close()
			authError(c, 503, "ARCHIVE_PACKAGE_INVALID")
			return
		}
		if size != expected.Size {
			_ = snapshot.Close()
			_ = writer.Close()
			authError(c, 503, "ARCHIVE_PACKAGE_INVALID")
			return
		}
		copyErr := appendFile(expected.Name, snapshot)
		closeErr := snapshot.Close()
		if err = errors.Join(copyErr, closeErr); err != nil {
			_ = writer.Close()
			authError(c, 403, "FORBIDDEN")
			return
		}
	}
	if err = writer.Close(); err != nil {
		authError(c, 503, "ARCHIVE_UNAVAILABLE")
		return
	}
	if err = check(c.Request.Context()); err != nil {
		authError(c, 403, "FORBIDDEN")
		return
	}
	stat, err := packageFile.Stat()
	if err != nil {
		authError(c, 503, "ARCHIVE_UNAVAILABLE")
		return
	}
	if _, err = packageFile.Seek(0, io.SeekStart); err != nil {
		authError(c, 503, "ARCHIVE_UNAVAILABLE")
		return
	}
	if _, err = archiveevidence.VerifyPackage(packageFile, stat.Size(), evidence.ManifestSha256); err != nil {
		authError(c, 503, "ARCHIVE_PACKAGE_INVALID")
		return
	}
	if _, err = packageFile.Seek(0, io.SeekStart); err != nil {
		authError(c, 503, "ARCHIVE_UNAVAILABLE")
		return
	}
	if !caseAudit(c, q, t, u, "DOWNLOAD archive.evidence_package", id) {
		return
	}
	stream := &evidenceDownload{source: packageFile, size: stat.Size(), release: release, guard: evidenceGuardedReader{ctx: c.Request.Context(), source: packageFile, check: check}}
	stream.complete = func(success bool, count int64) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		detail, _ := json.Marshal(map[string]any{"actor_id": u, "case_id": row.ID, "approval_id": id, "manifest_sha256": evidence.ManifestSha256, "bytes": count, "completed": success})
		_, _ = store.New(h.Platform.Pool).CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: t, Valid: true}, Action: "archive.evidence_transfer_finished", TargetType: pgtype.Text{String: "archive_approval", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(id, 10), Valid: true}, Detail: detail})
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=evidence-case-%d-approval-%d.zip", row.ID, id))
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Manifest-SHA256", evidence.ManifestSha256)
	c.Header("Accept-Ranges", "none")
	owned = true
	if err = middleware.DeferStream(c, stream, "application/zip", stat.Size()); err != nil {
		_ = stream.Close()
		_ = c.Error(err)
	}
}

package handlers

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	archiveStore "github.com/bushixiucai/urmail/internal/archive"
	"github.com/bushixiucai/urmail/internal/store"
)

// ArchiveContentHandler requires explicit tenant-owner grants even for owners.
type ArchiveContentHandler struct {
	Store *store.Queries
	Root  string
}

func (h *ArchiveContentHandler) authorized(c *gin.Context, scope string) (*store.Queries, int64, int64, bool) {
	tenant, ok := middleware.GetTenantID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return nil, 0, 0, false
	}
	user, ok := middleware.GetUserID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return nil, 0, 0, false
	}
	q := requestStore(c, h.Store)
	_, err := q.GetComplianceGrant(c, store.GetComplianceGrantParams{TenantID: tenant, UserID: user, RequiredScope: scope})
	if err != nil {
		code := "COMPLIANCE_GRANT_REQUIRED"
		if scope == "retention_manage" {
			code = "RETENTION_MANAGE_REQUIRED"
		}
		authError(c, 403, code)
		return nil, 0, 0, false
	}
	return q, tenant, user, true
}

func archiveAccessAudit(c *gin.Context, q *store.Queries, tenant, user int64, action, target string) bool {
	scope := "full_content"
	if action == "archive.export" {
		scope = "export"
	}
	if _, err := q.GetComplianceGrant(c, store.GetComplianceGrantParams{TenantID: tenant, UserID: user, RequiredScope: scope}); err != nil {
		authError(c, 403, "COMPLIANCE_GRANT_REQUIRED")
		return false
	}
	err := q.RecordTenantAudit(c, store.RecordTenantAuditParams{TenantID: tenant, UserID: user, Action: "READ " + action, Target: target, Status: 200})
	if err != nil {
		middleware.RecordAuditFailure(true)
		authError(c, 500, "DB_ERROR")
		return false
	}
	return true
}

// Content returns only plain text and MIME metadata, never active HTML.
func (h *ArchiveContentHandler) Content(c *gin.Context) { h.read(c, false) }

// Export requires a separate export grant and returns a verified original.
func (h *ArchiveContentHandler) Export(c *gin.Context) { h.read(c, true) }
func (h *ArchiveContentHandler) read(c *gin.Context, export bool) {
	scope := "full_content"
	if export {
		scope = "export"
	}
	q, tenant, user, ok := h.authorized(c, scope)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	entry, err := q.GetArchiveContent(c, store.GetArchiveContentParams{TenantID: tenant, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 404, "NOT_FOUND")
		return
	}
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	root := h.Root
	if root == "" {
		root = archiveStore.ArchiveRoot
	}
	if err = archiveStore.ValidateOwnershipPath(root, entry.Path, tenant, entry.MailboxID.Int64); err != nil {
		authError(c, 503, "ARCHIVE_INTEGRITY_UNAVAILABLE")
		return
	}
	if export {
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
		snapshot, size, openErr := archiveStore.SnapshotOriginal(c.Request.Context(), root, entry.Path, entry.RawSha256)
		if openErr != nil {
			authError(c, 503, "ARCHIVE_INTEGRITY_UNAVAILABLE")
			return
		}
		defer func() {
			if !owned {
				_ = snapshot.Close()
			}
		}()
		if !archiveAccessAudit(c, q, tenant, user, "archive.export", strconv.FormatInt(id, 10)) {
			_ = snapshot.Close()
			return
		}
		c.Header("Content-Disposition", "attachment; filename=archive-"+strconv.FormatInt(id, 10)+".eml")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Cache-Control", "no-store")
		stream := &archiveTransfer{ReadCloser: snapshot, release: release}
		owned = true
		if err = middleware.DeferStream(c, stream, "message/rfc822", size); err != nil {
			_ = stream.Close()
			_ = c.Error(err)
		}
		return
	}
	if err = archiveStore.VerifyOriginal(c.Request.Context(), root, entry.Path, entry.RawSha256); err != nil {
		authError(c, 503, "ARCHIVE_INTEGRITY_UNAVAILABLE")
		return
	}
	if !archiveAccessAudit(c, q, tenant, user, "archive.content_read", strconv.FormatInt(id, 10)) {
		return
	}
	var attachments any
	if json.Unmarshal(entry.Attachments, &attachments) != nil {
		authError(c, 503, "ARCHIVE_INTEGRITY_UNAVAILABLE")
		return
	}
	c.JSON(200, gin.H{"id": entry.ID, "body_text": entry.BodyText, "body_html": entry.BodyHtml, "attachments": attachments, "extraction_error": entry.ExtractionError, "raw_sha256": entry.RawSha256})
}

// Search searches extracted content under the same explicit grant and audit boundary.
func (h *ArchiveContentHandler) Search(c *gin.Context) {
	q, tenant, user, ok := h.authorized(c, "full_content")
	if !ok {
		return
	}
	query := strings.TrimSpace(c.Query("q"))
	from := strings.TrimSpace(c.Query("from_addr"))
	if len(query) > 500 || len(from) > 320 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	args := store.SearchArchiveContentParams{TenantID: tenant, Query: query, FromAddr: from, IncludeDeleted: c.Query("deleted") == "true", LimitCount: 101}
	if raw := c.Query("mailbox_id"); raw != "" {
		id, e := strconv.ParseInt(raw, 10, 64)
		if e != nil || id <= 0 {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		args.MailboxID = pgtype.Int8{Int64: id, Valid: true}
	}
	for _, field := range []struct {
		name   string
		target *pgtype.Timestamptz
	}{{"after", &args.AfterTime}, {"before", &args.BeforeTime}} {
		if raw := c.Query(field.name); raw != "" {
			parsed, e := time.Parse(time.RFC3339, raw)
			if e != nil {
				authError(c, 400, "INVALID_PARAMS")
				return
			}
			*field.target = pgtype.Timestamptz{Time: parsed, Valid: true}
		}
	}
	if args.AfterTime.Valid && args.BeforeTime.Valid && !args.BeforeTime.Time.After(args.AfterTime.Time) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	filter, _ := json.Marshal([]any{tenant, query, from, c.Query("mailbox_id"), c.Query("after"), c.Query("before"), args.IncludeDeleted})
	digest := fmt.Sprintf("%x", sha256.Sum256(filter))
	type archiveCursor struct {
		Time   time.Time `json:"time"`
		ID     int64     `json:"id"`
		Filter string    `json:"filter"`
	}
	if raw := c.Query("cursor"); raw != "" {
		if len(raw) > 1024 {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		encoded, e := base64.RawURLEncoding.DecodeString(raw)
		var cursor archiveCursor
		if e != nil || json.Unmarshal(encoded, &cursor) != nil || cursor.Time.IsZero() || cursor.ID <= 0 || cursor.Filter != digest {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		args.CursorTime = pgtype.Timestamptz{Time: cursor.Time, Valid: true}
		args.CursorID = pgtype.Int8{Int64: cursor.ID, Valid: true}
	}
	rows, err := q.SearchArchiveContent(c, args)
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	next := ""
	if len(rows) > 100 {
		rows = rows[:100]
		last := rows[len(rows)-1]
		encoded, _ := json.Marshal(archiveCursor{Time: last.ReceivedAt.Time, ID: last.ID, Filter: digest})
		next = base64.RawURLEncoding.EncodeToString(encoded)
	}
	if !archiveAccessAudit(c, q, tenant, user, "archive.content_search", "") {
		return
	}
	c.JSON(200, gin.H{"entries": rows, "next_cursor": next})
}

// RetentionPreview returns an expiring immutable candidate count and digest.
func (h *ArchiveContentHandler) RetentionPreview(c *gin.Context) {
	q, tenant, user, ok := h.authorized(c, "retention_manage")
	if !ok {
		return
	}
	var input struct {
		RetentionDays int32 `json:"retention_days"`
	}
	if c.ShouldBindJSON(&input) != nil || input.RetentionDays < 1 || input.RetentionDays > 36500 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	preview, err := q.PreviewArchiveRetention(c, store.PreviewArchiveRetentionParams{TenantID: tenant, RetentionDays: input.RetentionDays, CreatedBy: user})
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	c.JSON(200, gin.H{"preview": preview, "soft_delete": true, "physical_delete_after_days": 30, "hold_has_priority": true})
}

// RetentionConfirm activates only a fresh preview created by this same actor.
func (h *ArchiveContentHandler) RetentionConfirm(c *gin.Context) {
	q, tenant, user, ok := h.authorized(c, "retention_manage")
	if !ok {
		return
	}
	var input struct {
		PreviewID string `json:"preview_id"`
		Confirm   bool   `json:"confirm"`
	}
	var id pgtype.UUID
	if c.ShouldBindJSON(&input) != nil || !input.Confirm || id.Scan(input.PreviewID) != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	preview, err := q.LockArchiveRetentionPreview(c, store.LockArchiveRetentionPreviewParams{TenantID: tenant, ID: id, CreatedBy: user})
	if err != nil {
		authError(c, 409, "RETENTION_PREVIEW_STALE")
		return
	}
	cutoff := preview.CreatedAt.Time.Add(-time.Duration(preview.RetentionDays) * 24 * time.Hour)
	digest, err := q.ArchiveRetentionCandidateDigest(c, store.ArchiveRetentionCandidateDigestParams{TenantID: tenant, Cutoff: pgtype.Timestamptz{Time: cutoff, Valid: true}})
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	if digest.CandidateCount != preview.CandidateCount || digest.CandidateDigest != preview.CandidateDigest {
		authError(c, 409, "RETENTION_PREVIEW_STALE")
		return
	}
	if err = q.ConfirmArchiveRetentionPreview(c, store.ConfirmArchiveRetentionPreviewParams{TenantID: tenant, ID: id}); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	valid, grantErr := q.GetRetentionPolicyAuthority(c, store.GetRetentionPolicyAuthorityParams{TenantID: tenant, CreatedBy: user, GrantID: preview.AuthorizationGrantID.Int64})
	if grantErr != nil || !valid {
		authError(c, 403, "RETENTION_MANAGE_REQUIRED")
		return
	}
	if err = q.ActivateArchiveRetention(c, store.ActivateArchiveRetentionParams{TenantID: tenant, RetentionDays: preview.RetentionDays, CreatedBy: pgtype.Int8{Int64: user, Valid: true}, ConfirmedPreview: id, AuthorizationGrantID: preview.AuthorizationGrantID}); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "active", "physical_delete_after_days": 30})
}

// Attachment downloads an inert decoded part under full-content authorization.
func (h *ArchiveContentHandler) Attachment(c *gin.Context) {
	q, tenant, user, ok := h.authorized(c, "full_content")
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	index, err := strconv.Atoi(c.Param("attachment"))
	if err != nil || index < 0 || index > 199 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	entry, err := q.GetArchiveContent(c, store.GetArchiveContentParams{TenantID: tenant, ID: id})
	if err != nil {
		authError(c, 404, "NOT_FOUND")
		return
	}
	root := h.Root
	if root == "" {
		root = archiveStore.ArchiveRoot
	}
	if err = archiveStore.ValidateOwnershipPath(root, entry.Path, tenant, entry.MailboxID.Int64); err != nil {
		authError(c, 503, "ARCHIVE_INTEGRITY_UNAVAILABLE")
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
	raw, _, err := archiveStore.SnapshotOriginal(c.Request.Context(), root, entry.Path, entry.RawSha256)
	if err != nil {
		authError(c, 503, "ARCHIVE_INTEGRITY_UNAVAILABLE")
		return
	}
	defer func() { _ = raw.Close() }()
	part, data, err := archiveStore.SnapshotAttachment(c.Request.Context(), raw, index)
	if err != nil {
		authError(c, 404, "NOT_FOUND")
		return
	}
	defer func() {
		if !owned {
			_ = data.Close()
		}
	}()
	if !archiveAccessAudit(c, q, tenant, user, "archive.attachment_download", strconv.FormatInt(id, 10)+"/"+strconv.Itoa(index)) {
		_ = data.Close()
		return
	}
	c.Header("Content-Disposition", "attachment; filename=attachment-"+strconv.Itoa(index)+".bin")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "sandbox; default-src 'none'")
	c.Header("X-Archive-Attachment-SHA256", part.SHA256)
	c.Header("Cache-Control", "no-store")
	stream := &archiveTransfer{ReadCloser: data, release: release}
	owned = true
	if err = middleware.DeferStream(c, stream, "application/octet-stream", part.Size); err != nil {
		_ = stream.Close()
		_ = c.Error(err)
	}

}

// RetentionStatus exposes configuration, never archive body content.
func (h *ArchiveContentHandler) RetentionStatus(c *gin.Context) {
	tenant, ok := middleware.GetTenantID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	user, _ := middleware.GetUserID(c)
	q := requestStore(c, h.Store)
	_, grantErr := q.GetComplianceGrant(c, store.GetComplianceGrantParams{TenantID: tenant, UserID: user, RequiredScope: "retention_manage"})
	if grantErr != nil && !errors.Is(grantErr, pgx.ErrNoRows) {
		authError(c, 500, "DB_ERROR")
		return
	}
	canManage := grantErr == nil
	policy, err := q.GetRetentionPolicy(c, tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(200, gin.H{"active": false, "configured_active": false, "can_manage": canManage, "default": "permanent", "physical_delete_after_days": 30})
		return
	}
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	valid, err := q.GetRetentionPolicyAuthority(c, store.GetRetentionPolicyAuthorityParams{TenantID: tenant, CreatedBy: policy.CreatedBy.Int64, GrantID: policy.AuthorizationGrantID.Int64})
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	c.JSON(200, gin.H{"active": policy.Active && valid, "configured_active": policy.Active, "authorization_valid": valid, "can_manage": canManage, "retention_days": policy.RetentionDays, "physical_delete_after_days": 30, "confirmed_preview": policy.ConfirmedPreview})
}

// RetentionDisable always permits authorized tenant administrators to stop future cleanup.
func (h *ArchiveContentHandler) RetentionDisable(c *gin.Context) {
	tenant, ok := middleware.GetTenantID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	q := requestStore(c, h.Store)
	_, err := q.GetRetentionPolicy(c, tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(200, gin.H{"active": false})
		return
	}
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	err = q.DisableTenantRetentionPolicy(c, tenant)
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	c.JSON(200, gin.H{"active": false})
}

// Restore verifies that recoverable bytes exist before restoring a soft-deleted copy.
func (h *ArchiveContentHandler) Restore(c *gin.Context) {
	q, tenant, _, ok := h.authorized(c, "full_content")
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	entry, err := q.GetArchiveOriginalForRecovery(c, store.GetArchiveOriginalForRecoveryParams{TenantID: tenant, ID: id})
	if err != nil {
		authError(c, 409, "ARCHIVE_RESTORE_UNAVAILABLE")
		return
	}
	root := h.Root
	if root == "" {
		root = archiveStore.ArchiveRoot
	}
	if archiveStore.ValidateOwnershipPath(root, entry.Path, tenant, entry.MailboxID.Int64) != nil {
		authError(c, 503, "ARCHIVE_INTEGRITY_UNAVAILABLE")
		return
	}
	if err = archiveStore.VerifyOriginal(c.Request.Context(), root, entry.Path, entry.RawSha256); err != nil {
		authError(c, 503, "ARCHIVE_INTEGRITY_UNAVAILABLE")
		return
	}
	count, err := q.RestoreArchiveCopy(c, store.RestoreArchiveCopyParams{TenantID: tenant, ID: id})
	if err != nil || count != 1 {
		authError(c, 409, "ARCHIVE_RESTORE_UNAVAILABLE")
		return
	}
	c.JSON(200, gin.H{"status": "restored", "retention_policy_unchanged": true})
}

// Two transfers bound private temporary disk usage and concurrent MIME decoding.
var archiveTransferSlots = make(chan struct{}, 2)

func acquireArchiveTransfer(c *gin.Context) (func(), bool) {
	select {
	case archiveTransferSlots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-archiveTransferSlots }) }, true
	default:
		authError(c, http.StatusTooManyRequests, "RATE_LIMITED")
		return nil, false
	}
}

type archiveTransfer struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (r *archiveTransfer) Close() error {
	var err error
	r.once.Do(func() { err = r.ReadCloser.Close(); r.release() })
	return err
}

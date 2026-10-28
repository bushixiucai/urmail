package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/mailimport"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// MailboxImports keeps reviewed source facts separate from APPEND acceptance.
// A committed importing receipt always precedes the external IMAP operation.
type MailboxImports struct {
	Native *MailboxNativeHandler
	slots  chan struct{}
}

// RegisterMailboxImportRoutes exposes one bounded mailbox-owner workflow.
func RegisterMailboxImportRoutes(group *gin.RouterGroup, native *MailboxNativeHandler) {
	h := &MailboxImports{Native: native, slots: make(chan struct{}, 2)}
	limit := middleware.RateLimitWindow(120, time.Minute, func(c *gin.Context) (string, bool) {
		sess, ok := middleware.GetMailboxSession(c)
		return strconv.FormatInt(sess.TenantID, 10) + "/" + strconv.FormatInt(sess.MailboxID, 10), ok
	})
	r := group.Group("/imports", limit)
	r.GET("", h.List)
	r.GET("/:id", h.Get)
	r.POST("/preview", h.Preview)
	r.POST("/:id/confirm", h.Confirm)
	r.POST("/:id/resume", h.Resume)
	r.POST("/:id/reconcile", h.Reconcile)
	r.POST("/:id/retry", h.Retry)
	r.POST("/:id/cancel", h.Cancel)
	group.GET("/folders/:name/export.mbox", limit, h.ExportMBOX)
}

func importHash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func importDecode(c *gin.Context, value any, limit int64) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, limit))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		authError(c, 400, "MAIL_IMPORT_INVALID_SOURCE")
		return false
	}
	return true
}
func importDBError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 404, "NOT_FOUND")
	} else {
		authError(c, 503, "DB_ERROR")
	}
	return true
}
func (h *MailboxImports) principal(c *gin.Context) (webmail.Session, bool) {
	sess, ok := middleware.GetMailboxSession(c)
	if !ok || sess.TenantID <= 0 || sess.MailboxID <= 0 {
		authError(c, 401, "MAILBOX_AUTHORIZATION_DENIED")
		return sess, false
	}
	if h.Native == nil || !h.Native.Enabled || h.Native.Pool == nil || h.Native.Secrets == nil || h.Native.Auth == nil || h.Native.Sessions == nil {
		authError(c, 503, "MAIL_IMPORT_UNAVAILABLE")
		return sess, false
	}
	current, e := h.Native.Sessions.Get(c.Request.Context(), sess.TokenHash)
	if e != nil || current.TenantID != sess.TenantID || current.MailboxID != sess.MailboxID {
		authError(c, 401, "MAILBOX_AUTHORIZATION_DENIED")
		return sess, false
	}
	return sess, true
}
func (h *MailboxImports) fresh(c *gin.Context, sess webmail.Session, password string) bool {
	if password == "" || len(password) > 4096 {
		authError(c, 401, "MAIL_IMPORT_FRESH_AUTH_REQUIRED")
		return false
	}
	tenant, mailbox, address, hash, e := h.Native.Auth.Lookup(c.Request.Context(), sess.Address)
	if e != nil || tenant != sess.TenantID || mailbox != sess.MailboxID || address != sess.Address {
		authError(c, 401, "MAILBOX_AUTHORIZATION_DENIED")
		return false
	}
	valid, e := domainMail.VerifyPasswordContext(c.Request.Context(), password, hash)
	if e != nil {
		authError(c, 429, "AUTH_RATE_LIMITED")
		return false
	}
	if !valid {
		authError(c, 401, "MAIL_IMPORT_FRESH_AUTH_REQUIRED")
		return false
	}
	return true
}
func (h *MailboxImports) slot(c *gin.Context) bool {
	select {
	case h.slots <- struct{}{}:
		return true
	default:
		authError(c, 429, "MAIL_IMPORT_BUSY")
		return false
	}
}
func importID(c *gin.Context) (int64, bool) {
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		authError(c, 400, "INVALID_PARAMS")
		return 0, false
	}
	return id, true
}
func importSourceMatches(prior store.MailboxImportJobs, parsed mailimport.Preview, folder string, in mailboxImportPreviewRequest) bool {
	return prior.SourceDigest == parsed.SourceDigest && prior.Format == parsed.Format && prior.Filename == parsed.Filename && prior.Folder == folder && prior.UidValidity == int64(in.UIDValidity) && prior.DuplicatePolicy == in.DuplicatePolicy
}

func importItemDTO(item store.MailboxImportItems) gin.H {
	return gin.H{"index": item.ItemIndex, "subject": item.Subject, "message_id": item.MessageID, "size": item.RawBytes, "sha256": item.RawSha256, "status": item.Status, "error_code": item.ErrorCode, "duplicate_of": item.DuplicateOf, "duplicate_uids": item.BaselineUids, "selected": item.Selected, "destination_uid": item.DestinationUid, "attempts": item.Attempts, "reconciled_at": item.ReconciledAt, "reconcile_absent": item.ReconcileAbsent, "review_reason": item.ReviewReason}
}
func importAggregate(job store.MailboxImportJobs, items []store.MailboxImportItems) string {
	if job.Status == "preview" || job.Status == "canceled" || job.Status == "held" {
		return job.Status
	}
	if job.LeaseUntil.Valid && job.LeaseUntil.Time.After(time.Now()) && job.LeaseToken != "" {
		return "running"
	}
	pending, unknown, failed := false, false, false
	for _, item := range items {
		switch item.Status {
		case "pending":
			pending = true
		case "importing", "unknown":
			unknown = true
		case "failed":
			failed = true
		}
	}
	if unknown {
		return "uncertain"
	}
	if failed {
		return "partial"
	}
	if pending {
		return "confirmed"
	}
	return "completed"
}
func importJobDTO(job store.MailboxImportJobs, items []store.MailboxImportItems) gin.H {
	views := make([]gin.H, 0, len(items))
	counts := map[string]int{"ready": 0, "duplicate": 0, "invalid": 0, "pending": 0, "importing": 0, "imported": 0, "unknown": 0, "failed": 0, "skipped": 0, "canceled": 0}
	for _, item := range items {
		views = append(views, importItemDTO(item))
		counts[item.Status]++
	}
	return gin.H{"id": job.ID, "version": job.Version, "status": importAggregate(job, items), "format": job.Format, "filename": job.Filename, "folder_enc": webmail.EncodeFolderName(job.Folder), "uid_validity": job.UidValidity, "source_digest": job.SourceDigest, "preview_digest": job.PreviewDigest, "duplicate_policy": job.DuplicatePolicy, "bytes": job.SourceBytes, "total": len(items), "expires_at": job.PreviewExpiresAt.Time, "payload_expires_at": job.PayloadExpiresAt.Time, "confirmed_at": job.ConfirmedAt, "cancel_requested": job.CancelRequested, "counts": counts, "items": views}
}
func importLoad(ctx context.Context, q *store.Queries, sess webmail.Session, id int64, lock bool, payload ...bool) (store.MailboxImportJobs, []store.MailboxImportItems, error) {
	var job store.MailboxImportJobs
	var err error
	if lock {
		job, err = q.LockMailboxImportJob(ctx, store.LockMailboxImportJobParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, ID: id})
	} else {
		job, err = q.GetMailboxImportJob(ctx, store.GetMailboxImportJobParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, ID: id})
	}
	if err != nil {
		return job, nil, err
	}
	if len(payload) > 0 && !payload[0] {
		raw, e := q.MailboxImportItemViews(ctx, store.MailboxImportItemViewsParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, JobID: id})
		var items []store.MailboxImportItems
		if e == nil {
			e = json.Unmarshal(raw, &items)
		}
		return job, items, e
	}
	items, err := q.ListMailboxImportItems(ctx, store.ListMailboxImportItemsParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, JobID: id})
	return job, items, err
}
func (h *MailboxImports) respond(c *gin.Context, sess webmail.Session, id int64, status int) {
	tx, e := h.Native.Pool.MailboxTx(c.Request.Context(), sess.TenantID, sess.MailboxID)
	if importDBError(c, e) {
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	if e = q.MailboxImportRecoverExpiredLeases(c, store.MailboxImportRecoverExpiredLeasesParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID}); importDBError(c, e) {
		return
	}
	job, items, e := importLoad(c, q, sess, id, false, false)
	if importDBError(c, e) {
		return
	}
	if e = tx.Commit(c); importDBError(c, e) {
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(status, gin.H{"job": importJobDTO(job, items)})
}

// Get returns redacted, durable per-item facts after a browser restart.
func (h *MailboxImports) Get(c *gin.Context) {
	sess, ok := h.principal(c)
	if !ok {
		return
	}
	id, ok := importID(c)
	if ok {
		h.respond(c, sess, id, 200)
	}
}

// List never returns sealed MIME payloads or another mailbox's job identifiers.
func (h *MailboxImports) List(c *gin.Context) {
	sess, ok := h.principal(c)
	if !ok {
		return
	}
	tx, e := h.Native.Pool.MailboxTx(c, sess.TenantID, sess.MailboxID)
	if importDBError(c, e) {
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	if e = q.MailboxImportRecoverExpiredLeases(c, store.MailboxImportRecoverExpiredLeasesParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID}); importDBError(c, e) {
		return
	}
	jobs, e := q.ListMailboxImportJobs(c, store.ListMailboxImportJobsParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID})
	if importDBError(c, e) {
		return
	}
	views := make([]gin.H, 0, len(jobs))
	for _, job := range jobs {
		_, items, e := importLoad(c, q, sess, job.ID, false, false)
		if importDBError(c, e) {
			return
		}
		views = append(views, importJobDTO(job, items))
	}
	if e = tx.Commit(c); importDBError(c, e) {
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"jobs": views})
}

type mailboxImportPreviewRequest struct {
	Format          string `json:"format"`
	Filename        string `json:"filename"`
	ContentBase64   string `json:"content_base64"`
	FolderEnc       string `json:"folder_enc"`
	UIDValidity     uint32 `json:"uid_validity"`
	DuplicatePolicy string `json:"duplicate_policy"`
}

// Preview inventories exact destination copies, without ever APPENDing mail.
func (h *MailboxImports) Preview(c *gin.Context) {
	sess, ok := h.principal(c)
	if !ok || !h.slot(c) {
		return
	}
	defer func() { <-h.slots }()
	var in mailboxImportPreviewRequest
	if !importDecode(c, &in, 29<<20) {
		return
	}
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	folder, e := webmail.DecodeFolderName(in.FolderEnc)
	if e != nil || !webmail.ValidManagedFolder(folder) || in.UIDValidity == 0 || len(key) < 8 || len(key) > 200 || !slices.Contains([]string{"skip", "keep_both"}, in.DuplicatePolicy) {
		authError(c, 400, "MAIL_IMPORT_INVALID_SOURCE")
		return
	}
	source, e := base64.StdEncoding.Strict().DecodeString(in.ContentBase64)
	if e != nil || len(source) == 0 {
		authError(c, 400, "MAIL_IMPORT_INVALID_SOURCE")
		return
	}
	if len(source) > mailimport.MaxSourceBytes {
		authError(c, 413, "MAIL_IMPORT_SOURCE_TOO_LARGE")
		return
	}
	parsed, e := mailimport.Parse(in.Format, in.Filename, source)
	if e != nil {
		authError(c, 400, "MAIL_IMPORT_INVALID_SOURCE")
		return
	}
	// The request key freezes all source and target parameters before new I/O.
	q := store.New(h.Native.Pool)
	prior, e := q.GetMailboxImportByKey(c, store.GetMailboxImportByKeyParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, RequestKey: key})
	if e == nil {
		if !importSourceMatches(prior, parsed, folder, in) {
			authError(c, 409, "MAIL_IMPORT_SOURCE_CONFLICT")
			return
		}
		h.respond(c, sess, prior.ID, 200)
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		importDBError(c, e)
		return
	}
	client, _, ok := h.Native.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	if !mailboxExpectedValidity(c, client, folder, in.UIDValidity, true) {
		return
	}
	finder, ok := client.(webmail.ImportCopyFinder)
	if !ok {
		authError(c, 503, "MAIL_IMPORT_UNAVAILABLE")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	ctx = webmail.WithImportReadBudget(ctx, 32<<20)
	prepared := make([]store.CreateMailboxImportItemParams, 0, len(parsed.Items))
	seen := map[string][]int64{}
	for _, item := range parsed.Items {
		if len([]rune(item.Subject)) > 1000 {
			item.Subject = string([]rune(item.Subject)[:1000])
		}
		if len(item.MessageID) > 998 {
			item.MessageID = ""
		}
		row := store.CreateMailboxImportItemParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, ItemIndex: int32(item.Index), Subject: item.Subject, MessageID: item.MessageID, RawSha256: item.SHA256, RawBytes: item.Size, Status: "ready", ErrorCode: item.ErrorCode, DuplicateOf: int32(item.DuplicateOf), BaselineUids: []int64{}} // #nosec G115 -- parser indices are bounded to one hundred.
		if item.ErrorCode != "" {
			row.Status = "invalid"
			prepared = append(prepared, row)
			continue
		}
		found, seenBefore := seen[item.SHA256]
		if !seenBefore {
			matches, findErr := finder.FindImportCopies(ctx, folder, item.Raw, in.UIDValidity)
			if findErr != nil {
				row.Status = "invalid"
				row.ErrorCode = "MAIL_IMPORT_SCAN_UNAVAILABLE"
				prepared = append(prepared, row)
				continue
			}
			found = []int64{}
			for _, match := range matches {
				found = append(found, int64(match.UID))
			}
			seen[item.SHA256] = found
		}
		row.BaselineUids = found
		if in.DuplicatePolicy == "skip" && (len(found) > 0 || item.DuplicateOf > 0) {
			row.Status = "duplicate"
			row.ErrorCode = "MAIL_IMPORT_DUPLICATE"
		}
		if row.Status == "ready" {
			row.RawSecret, e = h.Native.Secrets.EncryptString(string(item.Raw))
			if e != nil {
				authError(c, 503, "MAIL_IMPORT_UNAVAILABLE")
				return
			}
		}
		prepared = append(prepared, row)
	}
	proofInput, _ := json.Marshal([]any{sess.TenantID, sess.MailboxID, parsed.SourceDigest, parsed.Format, parsed.Filename, folder, in.UIDValidity, in.DuplicatePolicy, prepared})
	proof := importHash(proofInput)
	tx, e := h.Native.Pool.MailboxTx(c, sess.TenantID, sess.MailboxID)
	if importDBError(c, e) {
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q = store.New(tx)
	if e = q.LockMailboxImportTenantBudget(c, sess.TenantID); importDBError(c, e) {
		return
	}
	if _, e = q.LockMailboxImportOwner(c, store.LockMailboxImportOwnerParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID}); importDBError(c, e) {
		return
	}
	if e = q.ClearExpiredMailboxImportPayloads(c, sess.TenantID); importDBError(c, e) {
		return
	}
	// Serialize key replay under the mailbox row lock before creating payloads.
	if existing, e := q.GetMailboxImportByKey(c, store.GetMailboxImportByKeyParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, RequestKey: key}); e == nil {
		if !importSourceMatches(existing, parsed, folder, in) {
			authError(c, 409, "MAIL_IMPORT_SOURCE_CONFLICT")
			return
		}

		if tx.Commit(c) != nil {
			authError(c, 503, "DB_ERROR")
			return
		}
		h.respond(c, sess, existing.ID, 200)
		return
	} else if !errors.Is(e, pgx.ErrNoRows) {
		importDBError(c, e)
		return
	}
	budget, e := q.MailboxImportStorageBudget(c, store.MailboxImportStorageBudgetParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID})
	if importDBError(c, e) {
		return
	}
	if budget.MailboxJobs >= 5 || budget.MailboxBytes+int64(len(source)) > 40<<20 || budget.TenantBytes+int64(len(source)) > 100<<20 {
		authError(c, 429, "MAIL_IMPORT_BUDGET_EXCEEDED")
		return
	}
	job, e := q.CreateMailboxImportJob(c, store.CreateMailboxImportJobParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, RequestKey: key, Format: parsed.Format, Filename: parsed.Filename, Folder: folder, UidValidity: int64(in.UIDValidity), SourceDigest: parsed.SourceDigest, PreviewDigest: proof, DuplicatePolicy: in.DuplicatePolicy, SourceBytes: int64(len(source))})
	if importDBError(c, e) {
		return
	}
	for _, row := range prepared {
		row.JobID = job.ID
		if e = q.CreateMailboxImportItem(c, row); importDBError(c, e) {
			return
		}
	}
	if e = tx.Commit(c); importDBError(c, e) {
		return
	}
	h.respond(c, sess, job.ID, 201)
}

func importUpdateItem(ctx context.Context, q *store.Queries, item store.MailboxImportItems, fence string, start, reconciled, clearPayload bool) (bool, error) {
	n, e := q.SetMailboxImportItem(ctx, store.SetMailboxImportItemParams{TenantID: item.TenantID, MailboxID: item.MailboxID, JobID: item.JobID, ItemIndex: item.ItemIndex, Selected: item.Selected, Status: item.Status, ErrorCode: item.ErrorCode, BaselineUids: item.BaselineUids, DestinationUid: item.DestinationUid, StartAttempt: start, Reconciled: reconciled, ReconcileAbsent: item.ReconcileAbsent, ReviewReason: item.ReviewReason, ClearPayload: clearPayload, LeaseFence: fence})
	return n == 1, e
}
func importUpdateJob(ctx context.Context, q *store.Queries, job store.MailboxImportJobs, status, token string, until pgtype.Timestamptz, confirm, cancel bool) (store.MailboxImportJobs, error) {
	return q.SetMailboxImportJob(ctx, store.SetMailboxImportJobParams{TenantID: job.TenantID, MailboxID: job.MailboxID, JobID: job.ID, ExpectedVersion: job.Version, Status: status, LeaseToken: token, LeaseUntil: until, Confirm: confirm, CancelRequested: cancel})
}

package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type mailboxImportAction struct {
	Version              int64   `json:"version"`
	PreviewDigest        string  `json:"preview_digest,omitempty"`
	SelectedIndices      []int32 `json:"selected_indices,omitempty"`
	Confirmed            bool    `json:"confirmed,omitempty"`
	CurrentPassword      string  `json:"current_password,omitempty"`
	Indices              []int32 `json:"indices,omitempty"`
	ConfirmedNotAppended bool    `json:"confirmed_not_appended,omitempty"`
	Reason               string  `json:"reason,omitempty"`
}

func importSelection(values []int32) (map[int32]bool, bool) {
	if len(values) == 0 || len(values) > 100 {
		return nil, false
	}
	result := map[int32]bool{}
	for _, v := range values {
		if v < 1 || v > 100 || result[v] {
			return nil, false
		}
		result[v] = true
	}
	return result, true
}
func importAudit(ctx context.Context, q *store.Queries, job store.MailboxImportJobs, action string, detail any) error {
	raw, e := json.Marshal(detail)
	if e != nil {
		return e
	}
	return q.RecordMailboxImportAudit(ctx, store.RecordMailboxImportAuditParams{TenantID: job.TenantID, MailboxID: job.MailboxID, JobID: job.ID, Action: "mailbox.import." + action, Detail: raw})
}
func (h *MailboxImports) action(c *gin.Context) (webmail.Session, int64, mailboxImportAction, bool) {
	sess, ok := h.principal(c)
	if !ok {
		return sess, 0, mailboxImportAction{}, false
	}
	id, ok := importID(c)
	if !ok {
		return sess, id, mailboxImportAction{}, false
	}
	var input mailboxImportAction
	if !importDecode(c, &input, 16384) || input.Version <= 0 {
		if !c.Writer.Written() {
			authError(c, 400, "INVALID_PARAMS")
		}
		return sess, id, input, false
	}
	return sess, id, input, true
}

// Confirm freezes the explicit item selection before any external append.
func (h *MailboxImports) Confirm(c *gin.Context) {
	sess, id, in, ok := h.action(c)
	if !ok {
		return
	}
	selection, ok := importSelection(in.SelectedIndices)
	if !ok || !in.Confirmed || in.PreviewDigest == "" {
		authError(c, 400, "MAIL_IMPORT_CONFIRM_REQUIRED")
		return
	}
	if !h.slot(c) {
		return
	}
	defer func() { <-h.slots }()
	if !h.fresh(c, sess, in.CurrentPassword) {
		return
	}
	tx, e := h.Native.Pool.MailboxTx(c, sess.TenantID, sess.MailboxID)
	if importDBError(c, e) {
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	quota, e := q.LockMailboxImportOwner(c, store.LockMailboxImportOwnerParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID})
	if importDBError(c, e) {
		return
	}
	job, items, e := importLoad(c, q, sess, id, true)
	if importDBError(c, e) {
		return
	}
	if job.PreviewDigest != in.PreviewDigest {
		authError(c, 409, "MAIL_IMPORT_SOURCE_CONFLICT")
		return
	}
	if job.Status != "preview" {
		frozen := map[int32]bool{}
		for _, item := range items {
			if item.Selected {
				frozen[item.ItemIndex] = true
			}
		}
		if len(frozen) != len(selection) {
			authError(c, 409, "MAIL_IMPORT_SOURCE_CONFLICT")
			return
		}
		for index := range selection {
			if !frozen[index] {
				authError(c, 409, "MAIL_IMPORT_SOURCE_CONFLICT")
				return
			}
		}
		if e = tx.Commit(c); importDBError(c, e) {
			return
		}
		h.run(c, sess, job, false)
		return
	}
	if job.Version != in.Version || !job.PreviewExpiresAt.Time.After(time.Now()) || !job.PayloadExpiresAt.Time.After(time.Now()) {
		authError(c, 409, "MAIL_IMPORT_PREVIEW_EXPIRED")
		return
	}
	total := int64(0)
	matched := 0
	for _, item := range items {
		if selection[item.ItemIndex] {
			if item.Status != "ready" || item.RawSecret == "" {
				authError(c, 409, "MAIL_IMPORT_SELECTION_INVALID")
				return
			}
			total += item.RawBytes
			matched++
		}
	}
	if matched != len(selection) {
		authError(c, 400, "MAIL_IMPORT_SELECTION_INVALID")
		return
	}
	reserved, e := q.MailboxImportReservedBytes(c, store.MailboxImportReservedBytesParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, ID: job.ID})
	if importDBError(c, e) {
		return
	}
	if quota.QuotaBytes > 0 && (total > quota.QuotaBytes-quota.BytesUsed-reserved) {
		authError(c, 409, "MAIL_IMPORT_QUOTA_EXCEEDED")
		return
	}
	for _, item := range items {
		item.Selected = selection[item.ItemIndex]
		if item.Selected {
			item.Status = "pending"
		} else if item.Status == "ready" || item.Status == "duplicate" {
			item.Status = "skipped"
			item.ErrorCode = "MAIL_IMPORT_NOT_SELECTED"
		}
		if _, e = importUpdateItem(c, q, item, "", false, false, !item.Selected); importDBError(c, e) {
			return
		}
	}
	job, e = importUpdateJob(c, q, job, "confirmed", "", pgtype.Timestamptz{}, true, false)
	if importDBError(c, e) {
		return
	}
	if e = importAudit(c, q, job, "confirm", gin.H{"selection": in.SelectedIndices, "preview_digest": job.PreviewDigest}); importDBError(c, e) {
		return
	}
	if e = tx.Commit(c); importDBError(c, e) {
		return
	}
	h.run(c, sess, job, false)
}

// Resume advances only already-confirmed pending items, never unknown attempts.
func (h *MailboxImports) Resume(c *gin.Context) {
	sess, id, in, ok := h.action(c)
	if !ok || !h.slot(c) {
		return
	}
	defer func() { <-h.slots }()
	if !h.fresh(c, sess, in.CurrentPassword) {
		return
	}
	job, e := store.New(h.Native.Pool).GetMailboxImportJob(c, store.GetMailboxImportJobParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, ID: id})
	if importDBError(c, e) {
		return
	}
	if job.Version != in.Version {
		authError(c, 409, "MAIL_IMPORT_VERSION_CONFLICT")
		return
	}
	h.run(c, sess, job, false)
}

// Reconcile reads complete destination bytes and never APPENDs anything.
func (h *MailboxImports) Reconcile(c *gin.Context) {
	sess, id, in, ok := h.action(c)
	if !ok || !h.slot(c) {
		return
	}
	defer func() { <-h.slots }()
	if !h.fresh(c, sess, in.CurrentPassword) {
		return
	}
	job, e := store.New(h.Native.Pool).GetMailboxImportJob(c, store.GetMailboxImportJobParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, ID: id})
	if importDBError(c, e) {
		return
	}
	if job.Version != in.Version {
		authError(c, 409, "MAIL_IMPORT_VERSION_CONFLICT")
		return
	}
	h.run(c, sess, job, true)
}

// Retry is an explicit, audited human statement after a recent absent scan.
func (h *MailboxImports) Retry(c *gin.Context) {
	sess, id, in, ok := h.action(c)
	if !ok || !h.slot(c) {
		return
	}
	defer func() { <-h.slots }()
	selection, valid := importSelection(in.Indices)
	if !valid || !in.ConfirmedNotAppended || len(strings.TrimSpace(in.Reason)) < 10 || len(in.Reason) > 2000 {
		authError(c, 400, "MAIL_IMPORT_REVIEW_REQUIRED")
		return
	}
	if !h.fresh(c, sess, in.CurrentPassword) {
		return
	}
	tx, e := h.Native.Pool.MailboxTx(c, sess.TenantID, sess.MailboxID)
	if importDBError(c, e) {
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	job, items, e := importLoad(c, q, sess, id, true)
	if importDBError(c, e) {
		return
	}
	if job.Version != in.Version || !job.ConfirmedAt.Valid || job.Status == "preview" || job.Status == "canceled" || job.LeaseUntil.Valid && job.LeaseUntil.Time.After(time.Now()) {
		authError(c, 409, "MAIL_IMPORT_VERSION_CONFLICT")
		return
	}
	matches := 0
	for _, item := range items {
		if !selection[item.ItemIndex] {
			continue
		}
		matches++
		if !item.Selected || !slices.Contains([]string{"unknown", "failed"}, item.Status) || item.RawSecret == "" || item.Attempts >= 20 || !item.ReconcileAbsent || !item.ReconciledAt.Valid || time.Since(item.ReconciledAt.Time) < 0 || time.Since(item.ReconciledAt.Time) > 5*time.Minute {
			authError(c, 409, "MAIL_IMPORT_REVIEW_REQUIRED")
			return
		}
	}
	if matches != len(selection) {
		authError(c, 400, "MAIL_IMPORT_SELECTION_INVALID")
		return
	}
	for _, item := range items {
		if selection[item.ItemIndex] {
			item.Status = "pending"
			item.ErrorCode = ""
			item.ReconcileAbsent = false
			item.ReviewReason = in.Reason
			if _, e = importUpdateItem(c, q, item, "", false, false, false); importDBError(c, e) {
				return
			}
		}
	}
	job, e = importUpdateJob(c, q, job, "confirmed", "", pgtype.Timestamptz{}, false, false)
	if importDBError(c, e) {
		return
	}
	if e = importAudit(c, q, job, "review_retry", gin.H{"indices": in.Indices, "reason": in.Reason, "confirmed_not_appended": true}); importDBError(c, e) {
		return
	}
	if e = tx.Commit(c); importDBError(c, e) {
		return
	}
	h.run(c, sess, job, false)
}

// Cancel prevents future claims; accepted and unknown external facts remain.
func (h *MailboxImports) Cancel(c *gin.Context) {
	sess, id, in, ok := h.action(c)
	if !ok {
		return
	}
	tx, e := h.Native.Pool.MailboxTx(c, sess.TenantID, sess.MailboxID)
	if importDBError(c, e) {
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	job, items, e := importLoad(c, q, sess, id, true)
	if importDBError(c, e) {
		return
	}
	if job.Version != in.Version {
		authError(c, 409, "MAIL_IMPORT_VERSION_CONFLICT")
		return
	}
	for _, item := range items {
		clearPayload := true
		switch item.Status {
		case "ready", "pending", "duplicate", "failed":
			item.Status = "canceled"
			item.ErrorCode = "MAIL_IMPORT_CANCELED"
		case "importing":
			item.Status = "unknown"
			item.ErrorCode = "MAIL_IMPORT_ACK_UNKNOWN"
			clearPayload = false
		case "unknown":
			clearPayload = false
		}
		if _, e = importUpdateItem(c, q, item, "", false, false, clearPayload); importDBError(c, e) {
			return
		}
	}
	job, e = importUpdateJob(c, q, job, "canceled", "", pgtype.Timestamptz{}, false, true)
	if importDBError(c, e) {
		return
	}
	if e = importAudit(c, q, job, "cancel", gin.H{"in_flight_may_have_appended": true}); importDBError(c, e) {
		return
	}
	if e = tx.Commit(c); importDBError(c, e) {
		return
	}
	h.respond(c, sess, id, 200)
}

func (h *MailboxImports) run(c *gin.Context, sess webmail.Session, expected store.MailboxImportJobs, reconcile bool) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 65*time.Second)
	defer cancel()
	ctx = webmail.WithImportReadBudget(ctx, 32<<20)
	tx, e := h.Native.Pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if importDBError(c, e) {
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	if e = q.MailboxImportRecoverExpiredLeases(ctx, store.MailboxImportRecoverExpiredLeasesParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID}); importDBError(c, e) {
		return
	}
	job, items, e := importLoad(ctx, q, sess, expected.ID, true)
	if importDBError(c, e) {
		return
	}
	if job.Version != expected.Version {
		authError(c, 409, "MAIL_IMPORT_VERSION_CONFLICT")
		return
	}
	if !job.ConfirmedAt.Valid || job.Status == "preview" || job.Status == "held" && !reconcile || job.CancelRequested && !reconcile {
		authError(c, 409, "MAIL_IMPORT_REVIEW_REQUIRED")
		return
	}
	if job.LeaseUntil.Valid && job.LeaseUntil.Time.After(time.Now()) {
		authError(c, 409, "MAIL_IMPORT_BUSY")
		return
	}
	eligible := false
	for _, item := range items {
		if reconcile && (item.Status == "unknown" || item.Status == "failed") || !reconcile && item.Status == "pending" {
			eligible = true
			break
		}
	}
	if !eligible {
		if e = tx.Commit(ctx); importDBError(c, e) {
			return
		}
		h.respond(c, sess, job.ID, 200)
		return
	}
	token := uuid.NewString()
	originalStatus := job.Status
	job, e = importUpdateJob(ctx, q, job, job.Status, token, pgtype.Timestamptz{Time: time.Now().Add(90 * time.Second), Valid: true}, false, job.CancelRequested)
	if importDBError(c, e) {
		return
	}
	if e = tx.Commit(ctx); importDBError(c, e) {
		return
	}
	defer h.release(c, sess, job, token, originalStatus)
	client, _, ok := h.Native.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	if !mailboxExpectedValidity(c, client, job.Folder, uint32(job.UidValidity), true) {
		return
	} // #nosec G115 -- persisted generation constrained to uint32.
	finder, ok := client.(webmail.ImportCopyFinder)
	if !ok {
		authError(c, 503, "MAIL_IMPORT_UNAVAILABLE")
		return
	}
	importer, ok := client.(webmail.MessageImporter)
	if !ok {
		authError(c, 503, "MAIL_IMPORT_UNAVAILABLE")
		return
	}
	handled := 0
	for _, item := range items {
		if handled >= 5 || ctx.Err() != nil {
			break
		}
		if reconcile {
			if item.Status != "unknown" && item.Status != "failed" {
				continue
			}
		} else if item.Status != "pending" {
			continue
		}
		handled++
		raw, e := h.Native.Secrets.DecryptString(item.RawSecret)
		if e != nil || item.RawSecret == "" || importHash([]byte(raw)) != item.RawSha256 || int64(len(raw)) != item.RawBytes {
			if item.Status != "unknown" {
				item.Status = "failed"
			}
			item.ReconcileAbsent = false
			item.ErrorCode = "MAIL_IMPORT_PAYLOAD_EXPIRED"
			if !h.saveItem(ctx, sess, item, token, false, false, false) {
				break
			}
			continue
		}
		matches, e := finder.FindImportCopies(ctx, job.Folder, []byte(raw), uint32(job.UidValidity)) // #nosec G115 -- persisted generation constrained to uint32.
		if e != nil {
			item.ReconcileAbsent = false
			item.ErrorCode = "MAIL_IMPORT_SCAN_UNAVAILABLE"
			if !h.saveItem(ctx, sess, item, token, false, false, false) {
				break
			}
			continue
		}
		observed := make([]int64, 0, len(matches))
		for _, match := range matches {
			observed = append(observed, int64(match.UID))
		}
		if reconcile {
			item.ReconcileAbsent = true
			item.ErrorCode = "MAIL_IMPORT_NOT_OBSERVED"
			for _, uid := range observed {
				if !slices.Contains(item.BaselineUids, uid) {
					item.Status = "imported"
					item.DestinationUid = pgtype.Int8{Int64: uid, Valid: true}
					item.ReconcileAbsent = false
					item.ErrorCode = "MAIL_IMPORT_COPY_VERIFIED"
					break
				}
			}
			if !h.saveItem(ctx, sess, item, token, false, true, item.Status == "imported") {
				break
			}
			continue
		}
		if item.Attempts > 0 && item.ReviewReason != "" {
			lateCopy := int64(0)
			for _, uid := range observed {
				if !slices.Contains(item.BaselineUids, uid) {
					lateCopy = uid
					break
				}
			}
			if lateCopy > 0 {
				item.Status = "imported"
				item.ErrorCode = "MAIL_IMPORT_COPY_VERIFIED"
				item.DestinationUid = pgtype.Int8{Int64: lateCopy, Valid: true}
				if !h.saveItem(ctx, sess, item, token, false, true, true) {
					break
				}
				continue
			}
		}
		if job.DuplicatePolicy == "skip" && len(observed) > 0 {
			item.Status = "skipped"
			item.ErrorCode = "MAIL_IMPORT_DUPLICATE"
			item.BaselineUids = observed
			if !h.saveItem(ctx, sess, item, token, false, false, true) {
				break
			}
			continue
		}
		if _, ok = h.principal(c); !ok {
			break
		}
		item.BaselineUids = observed
		item.Status = "importing"
		item.ErrorCode = ""
		item.ReconcileAbsent = false
		if !h.claimItem(ctx, c, sess, job, item, token) {
			break
		}
		outcome, appendErr := importer.ImportMessage(ctx, job.Folder, []byte(raw))
		item.Status = "unknown"
		item.ErrorCode = "MAIL_IMPORT_ACK_UNKNOWN"
		if appendErr == nil && outcome.UID != 0 && int64(outcome.UIDValidity) == job.UidValidity {
			accessor, available := client.(webmail.RawMessageAccessor)
			if available {
				stored, validity, verifyErr := accessor.GetRawMessage(ctx, job.Folder, outcome.UID)
				if verifyErr == nil && int64(validity) == job.UidValidity && importHash(stored) == item.RawSha256 {
					item.Status = "imported"
					item.ErrorCode = ""
					item.DestinationUid = pgtype.Int8{Int64: int64(outcome.UID), Valid: true}
				}
			}
		}
		if !h.saveItem(context.WithoutCancel(ctx), sess, item, token, false, false, item.Status == "imported") {
			break
		}
		if item.Status == "unknown" {
			break
		}
	}
}
func (h *MailboxImports) saveItem(ctx context.Context, sess webmail.Session, item store.MailboxImportItems, token string, start, reconciled, clearPayload bool) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tx, e := h.Native.Pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if e != nil {
		return false
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	current, e := q.LockMailboxImportJob(ctx, store.LockMailboxImportJobParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, ID: item.JobID})
	if e != nil || current.LeaseToken != token || !current.LeaseUntil.Valid || !current.LeaseUntil.Time.After(time.Now()) {
		return false
	}
	valid, e := importUpdateItem(ctx, q, item, token, start, reconciled, clearPayload)
	if e != nil || !valid {
		return false
	}
	job, e := q.GetMailboxImportJob(ctx, store.GetMailboxImportJobParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, ID: item.JobID})
	if e != nil {
		return false
	}
	if e = importAudit(ctx, q, job, "item", gin.H{"index": item.ItemIndex, "status": item.Status, "error_code": item.ErrorCode, "uid": item.DestinationUid}); e != nil {
		return false
	}
	return tx.Commit(ctx) == nil
}
func (h *MailboxImports) claimItem(ctx context.Context, c *gin.Context, sess webmail.Session, expected store.MailboxImportJobs, item store.MailboxImportItems, token string) bool {
	tx, e := h.Native.Pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if importDBError(c, e) {
		return false
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	quota, e := q.LockMailboxImportOwner(ctx, store.LockMailboxImportOwnerParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID})
	if importDBError(c, e) {
		return false
	}
	job, items, e := importLoad(ctx, q, sess, expected.ID, true)
	if importDBError(c, e) {
		return false
	}
	if job.LeaseToken != token || job.CancelRequested || !job.LeaseUntil.Valid || !job.LeaseUntil.Time.After(time.Now()) {
		return false
	}
	ready := false
	for _, current := range items {
		if current.ItemIndex == item.ItemIndex && current.Status == "pending" {
			ready = true
		}
	}
	if !ready {
		return false
	}
	if quota.QuotaBytes > 0 && item.RawBytes > quota.QuotaBytes-quota.BytesUsed {
		authError(c, 409, "MAIL_IMPORT_QUOTA_EXCEEDED")
		return false
	}
	valid, e := importUpdateItem(ctx, q, item, token, true, false, false)
	if importDBError(c, e) || !valid {
		return false
	}
	if e = importAudit(ctx, q, job, "claim", gin.H{"index": item.ItemIndex, "sha256": item.RawSha256, "baseline_uids": item.BaselineUids}); importDBError(c, e) {
		return false
	}
	return tx.Commit(ctx) == nil
}
func (h *MailboxImports) release(c *gin.Context, sess webmail.Session, expected store.MailboxImportJobs, token, originalStatus string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, e := h.Native.Pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if e != nil {
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	job, items, e := importLoad(ctx, q, sess, expected.ID, true)
	if e != nil {
		return
	}
	if job.LeaseToken == token {
		job.LeaseUntil = pgtype.Timestamptz{}
		job.LeaseToken = ""
		state := importAggregate(job, items)
		if originalStatus == "held" || originalStatus == "canceled" {
			state = originalStatus
		}
		job, e = importUpdateJob(ctx, q, job, state, "", pgtype.Timestamptz{}, false, job.CancelRequested)
		if e != nil {
			return
		}
	}
	if tx.Commit(ctx) != nil {
		return
	}
	if !c.Writer.Written() {
		status := http.StatusOK
		for _, item := range items {
			if item.Status == "pending" || item.Status == "importing" {
				status = http.StatusAccepted
				break
			}
		}
		h.respond(c, sess, expected.ID, status)
	}
}

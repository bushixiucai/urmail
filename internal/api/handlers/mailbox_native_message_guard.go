package handlers

import (
	"context"
	"sort"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/mailboxsend"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// guardDraftMutation holds the submission lock through a generic IMAP mutation.
// A submitted draft cannot escape its receipt by being copied, moved or removed
// through the inbox endpoints instead of the dedicated draft API.
func (h *MailboxNativeHandler) guardDraftMutation(c *gin.Context, client webmail.Client, session webmail.Session, folder string, validity uint32, uids []uint32, deleting bool, folders []webmail.Folder) (func(), bool) {
	release := func() {}
	if folders == nil {
		var err error
		folders, err = client.ListFolders(c.Request.Context())
		if err != nil {
			authError(c, 502, "IMAP_UNAVAILABLE")
			return release, false
		}
	}
	source, found := folderInList(folders, folder)
	if !found || !webmail.IsDraftsFolder(source) {
		return release, true
	}
	if h.Pool == nil {
		authError(c, 503, "DB_ERROR")
		return release, false
	}
	tx, err := h.Pool.MailboxTx(c.Request.Context(), session.TenantID, session.MailboxID)
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return release, false
	}
	release = func() { _ = tx.Rollback(context.Background()) }
	ordered := append([]uint32(nil), uids...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	q := store.New(tx)
	for _, uid := range ordered {
		if err = mailboxsend.LockDraft(c.Request.Context(), q, session.TenantID, session.MailboxID, mailboxsend.DraftRef{Folder: folder, UID: uid, Validity: validity}); err != nil {
			release()
			authError(c, 503, "DB_ERROR")
			return func() {}, false
		}
	}
	for _, uid := range ordered {
		if deleting && h.draftDeletionLocked(c, folder, uid, validity, q) || !deleting && h.draftSubmissionLocked(c, folder, uid, validity, q) {
			release()
			return func() {}, false
		}
		pending, pendingErr := q.PlatformDraftReplacementPending(c.Request.Context(), store.PlatformDraftReplacementPendingParams{TenantID: session.TenantID, MailboxID: session.MailboxID, PreviousUid: strconv.FormatUint(uint64(uid), 10), PreviousValidity: strconv.FormatUint(uint64(validity), 10)})
		if pendingErr != nil {
			release()
			authError(c, 503, "DB_ERROR")
			return func() {}, false
		}
		if pending {
			release()
			authError(c, 409, "DRAFT_SAVE_UNCERTAIN")
			return func() {}, false
		}
	}
	return release, true
}

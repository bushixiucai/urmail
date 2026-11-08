package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/mailauth"
	"github.com/bushixiucai/urmail/internal/rendercache"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// ListMessages returns a cursor-anchored page of UID summaries for a folder.
//
// Query params:
//   - limit, offset — pagination (clamped by webmail.ClampListOpts)
//   - q — optional search string; when set, IMAP UID SEARCH replaces ALL.
//     See webmail.BuildSearchCriteria for supported forms (subject:/from:/
//     text:/is:unseen and bare text → OR SUBJECT/FROM). Invalid q → 400.
//   - sort — date|subject|from|size ("" = arrival order). Invalid → 400.
//     Sorts map to IMAP SORT when advertised; otherwise the summaries are
//     sorted server-side as a fallback (response flag server_sorted=false).
//   - order — "asc"|"desc" (default desc for every sort key).
//   - cursor — UID anchor from next_cursor; takes precedence over offset and
//     keeps pagination stable while new mail arrives (webmail.ListOpts
//     documents the contract). Expunged cursors resync from the head.
//   - thread — "1"/"true" to stamp thread_id/thread_count (server path with
//     THREAD=REFERENCES, client grouping otherwise; response thread_mode and
//     thread_scope, the latter reporting how complete the counts are).
//   - preview — "0"/"false" drops the one-line body snippet from every row.
//     By default each message carries a plain-text snippet (preview) fetched
//     with one batched, byte-bounded IMAP command per page.
func (h *MailboxNativeHandler) ListMessages(c *gin.Context) {
	folder, ok := h.folderParam(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	q := strings.TrimSpace(c.Query("q"))
	sortBy, err := webmail.NormalizeSortKey(c.Query("sort"))
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	order := strings.ToLower(strings.TrimSpace(c.Query("order")))
	sortDesc := order != "asc"
	cursor64, _ := strconv.ParseUint(c.Query("cursor"), 10, 32)
	thread := isTruthyParam(c.Query("thread"))
	// Validate before dialing so bad q fails closed without an IMAP round-trip.
	if _, err := webmail.BuildSearchCriteria(q); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_SEARCH_QUERY")
		return
	}
	client, _, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()

	opts := webmail.ClampListOpts(webmail.ListOpts{
		Limit:     limit,
		Offset:    offset,
		Query:     q,
		SortBy:    sortBy,
		SortDesc:  sortDesc,
		CursorUID: uint32(cursor64),
		Thread:    thread,
		Preview:   !isDisabledParam(c.Query("preview")),
	})
	result, err := client.ListMessages(c.Request.Context(), folder, opts)
	if err != nil {
		if errors.Is(err, webmail.ErrInvalidSearchQuery) {
			authError(c, http.StatusBadRequest, "INVALID_SEARCH_QUERY")
			return
		}
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	if result.Messages == nil {
		result.Messages = []webmail.MessageSummary{}
	}
	for i := range result.Messages {
		result.Messages[i].UIDValidity = result.UIDValidity
		// Folder/FolderEnc stay empty here: they tag cross-folder search
		// results only. A single-folder list showing "INBOX" on every row
		// contradicts the webmail UX spec (audit P0-7).
	}
	c.JSON(http.StatusOK, gin.H{
		"messages":      result.Messages,
		"uid_validity":  result.UIDValidity,
		"total":         result.Total,
		"limit":         opts.Limit,
		"offset":        opts.Offset,
		"q":             q,
		"next_cursor":   result.NextCursor,
		"sort":          result.Sort,
		"server_sorted": result.ServerSorted,
		"thread_mode":   result.ThreadMode,
		"thread_scope":  result.ThreadScope,
	})
}

// isTruthyParam parses checkbox-style query flags.
func isTruthyParam(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// isDisabledParam reports whether a default-on query flag was explicitly
// switched off ("0", "false", "off", "no").
func isDisabledParam(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "off", "no":
		return true
	}
	return false
}

// attachmentContentDisposition builds a Content-Disposition header from an
// untrusted MIME filename: path traversal, separators, and control characters
// are stripped, and the result is RFC 2183/5987 encoded by mime.FormatMediaType.
func attachmentContentDisposition(filename, partID string) string {
	safeName := path.Base(strings.ReplaceAll(filename, "\\", "/"))
	safeName = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, safeName)
	if safeName == "." || safeName == "/" || safeName == "" {
		safeName = "attachment-" + partID
	}
	if disp := mime.FormatMediaType("attachment", map[string]string{"filename": safeName}); disp != "" {
		return disp
	}
	return "attachment; filename=\"attachment-" + partID + "\""
}

// cidAttachmentURL maps a Content-ID to the same-origin attachment endpoint
// for this message; ok is false when no surfaced part carries that cid.
func cidAttachmentURL(folder string, uid uint32, atts []webmail.AttachmentMeta, validity ...uint32) func(cid string) (string, bool) {
	byID := make(map[string]string, len(atts))
	for _, a := range atts {
		if a.ContentID != "" {
			byID[strings.Trim(a.ContentID, "<> ")] = a.PartID
		}
	}
	return func(cid string) (string, bool) {
		partID, ok := byID[strings.Trim(cid, "<> ")]
		if !ok {
			return "", false
		}
		url := "/api/v1/mailbox/folders/" + webmail.EncodeFolderName(folder) +
			"/messages/" + strconv.FormatUint(uint64(uid), 10) +
			"/attachments/" + partID
		if len(validity) > 0 && validity[0] != 0 {
			url += "?uid_validity=" + strconv.FormatUint(uint64(validity[0]), 10)
		}
		return url, true
	}
}

// GetMessage returns body text/html and attachment metadata for one UID.
func (h *MailboxNativeHandler) GetMessage(c *gin.Context) {
	folder, ok := h.folderParam(c)
	if !ok {
		return
	}
	uid, err := strconv.ParseUint(c.Param("uid"), 10, 32)
	if err != nil || uid == 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	loadImages := isTruthyParam(c.Query("load_images"))
	uaClass := webmail.ParseUAClass(c.GetHeader("X-UA-Class"))
	vpWidth := webmail.ParseViewportWidth(c.GetHeader("X-Viewport-Width"))
	vpBucket := webmail.ViewportBucket(vpWidth)
	var expectedValidity uint32
	if value := c.Query("uid_validity"); value != "" {
		v, parseErr := strconv.ParseUint(value, 10, 32)
		if parseErr != nil || v == 0 {
			authError(c, http.StatusBadRequest, "INVALID_PARAMS")
			return
		}
		expectedValidity = uint32(v)
	}
	// Cache hit paths. With uid_validity in the request the exact key
	// proves the folder was not renumbered. Without it a loose match
	// (every dimension except uid_validity) still serves the cached
	// rendering: the payload echoes the uid_validity the entry was
	// rendered with so the client can detect a renumber, and the TTL
	// bounds how long one could go unnoticed server-side (DEF-3: the
	// previous hard requirement made every request without uid_validity
	// pay the full IMAP FETCH + sanitize again).
	if h.RenderCache != nil {
		if sessionPeek, ok := middleware.GetMailboxSession(c); ok {
			cacheKey := rendercache.Key{
				MailboxID:      sessionPeek.MailboxID,
				Folder:         folder,
				UID:            uint32(uid),
				UIDValidity:    expectedValidity,
				LoadImages:     loadImages && len(h.RenderKey) > 0,
				UAClass:        uaClass,
				ViewportBucket: vpBucket,
			}
			var (
				entry rendercache.Entry
				hit   bool
			)
			if expectedValidity != 0 {
				entry, hit = h.RenderCache.Get(cacheKey)
			} else {
				entry, hit = h.RenderCache.GetLoose(cacheKey)
			}
			if hit {
				serveMessagePayload(c, entry.Payload, "hit")
				return
			}
		}
	}
	client, session, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	if expectedValidity != 0 {
		actual, readErr := client.FolderUIDValidity(c.Request.Context(), folder)
		if readErr != nil || uint64(actual) != uint64(expectedValidity) {
			authError(c, 409, "AI_SOURCE_CHANGED")
			return
		}
	}
	msg, err := client.GetMessage(c.Request.Context(), folder, uint32(uid))
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			authError(c, http.StatusNotFound, "MESSAGE_NOT_FOUND")
			return
		}
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	if expectedValidity != 0 && expectedValidity != msg.UIDValidity {
		authError(c, 409, "AI_SOURCE_CHANGED")
		return
	}
	if msg.Authentication.Status == "" {
		msg.Authentication = mailauth.Result{Status: "unknown", Reason: "receive_proof_missing"}
	}
	if msg.Attachments == nil {
		msg.Attachments = []webmail.AttachmentMeta{}
	}
	if msg.HTML != "" {
		// Render-time sanitization (WEBMAIL-007): the IMAP original stays
		// untouched. Remote images are blocked by default for privacy;
		// ?load_images=1 is the per-message "load images" user action.
		//
		// When the reader allows remote images, they are served through
		// the server-side image proxy: the browser must not contact the
		// third party, or a tracking pixel learns the reader's address.
		// Without a signing key the proxy cannot sign a URL, so remote
		// images stay blocked (fail closed) instead of being fetched by
		// the browser directly.
		allowRemote := loadImages && len(h.RenderKey) > 0
		sanitizeOpts := webmail.SanitizeOptions{
			AllowRemoteImages: allowRemote,
			CIDRewriter:       cidAttachmentURL(folder, uint32(uid), msg.Attachments, msg.UIDValidity),
		}
		if allowRemote {
			sanitizeOpts.RemoteImageProxy = h.imageProxyRewriter(session.MailboxID, uint32(uid))
		}
		if len(h.RenderKey) > 0 {
			from := ""
			if len(msg.From) > 0 {
				if msg.From[0].Name != "" {
					from = msg.From[0].Name + " <" + msg.From[0].Email + ">"
				} else {
					from = msg.From[0].Email
				}
			}
			sanitizeOpts.LinkRewriter = h.linkConfirmRewriter(session.MailboxID, uint32(uid), from, msg.Subject)
		}
		// Style-selector inlining precedes sanitization (inline →
		// sanitize → adapt): <style> rules would otherwise die in the
		// sanitizer before touching an element. Media queries are
		// evaluated server-side against the reading bucket, so the
		// rendercache key's bucket fully determines the output.
		inlined := webmail.InlineCSS(msg.HTML, uaClass)
		safe, notes := webmail.SanitizeHTMLOpts(inlined, sanitizeOpts)
		vp := webmail.Viewport{UAClass: uaClass, Width: vpWidth}
		adapted := webmail.AdaptHTMLForViewport(safe, vp)
		msg.HTML = webmail.WrapHTMLDocument(adapted)
		msg.Notes = notes
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		authError(c, http.StatusInternalServerError, "INTERNAL")
		return
	}
	xCache := ""
	if h.RenderCache != nil {
		h.RenderCache.Put(rendercache.Key{
			MailboxID:      session.MailboxID,
			Folder:         folder,
			UID:            uint32(uid),
			UIDValidity:    msg.UIDValidity,
			LoadImages:     loadImages && len(h.RenderKey) > 0,
			UAClass:        uaClass,
			ViewportBucket: vpBucket,
		}, payload)
		xCache = "miss"
	}
	serveMessagePayload(c, payload, xCache)
}

// serveMessagePayload writes one GetMessage body under the conditional-
// request contract (v0.6.12 PR-7): every 200 carries a strong ETag (the
// sha256 of the exact payload bytes) and, when the render cache is
// enabled, X-Render-Cache: hit|miss so QA can tell a served rendering
// from a fresh render in the access logs. A request whose If-None-Match
// matches — weak comparison per RFC 9110 §8.8.3, list and * accepted —
// gets a header-only 304 with no body; the client keeps the bytes it
// already holds (Cache-Control stays no-store, so nothing is stored by
// any HTTP cache and the conditional round trip remains an opt-in
// agreement between the webmail frontend and this API).
func serveMessagePayload(c *gin.Context, payload []byte, xCache string) {
	etag := payloadETag(payload)
	c.Header("Cache-Control", "no-store")
	if etag != "" {
		c.Header("ETag", etag)
	}
	if xCache != "" {
		c.Header("X-Render-Cache", xCache)
	}
	if etagMatches(c.GetHeader("If-None-Match"), etag) {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", payload)
}

// payloadETag derives the strong entity tag of a rendered message body.
// Cache hits replay the exact bytes, so a hit and a fresh render of the
// same content agree on the tag.
func payloadETag(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	sum := sha256.Sum256(payload)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// etagMatches evaluates If-None-Match against the current tag: a
// comma-separated list, weak validators compared weakly (the W/ prefix
// is stripped on both sides), * matches any existing representation.
func etagMatches(header, etag string) bool {
	if header == "" || etag == "" {
		return false
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		candidate = strings.TrimPrefix(candidate, "W/")
		current := strings.TrimPrefix(etag, "W/")
		if candidate == "*" || candidate == current {
			return true
		}
	}
	return false
}

// GetAttachment streams one MIME part as a downloadable attachment.
// GET /mailbox/folders/:name/messages/:uid/attachments/:part_id
func (h *MailboxNativeHandler) GetAttachment(c *gin.Context) {
	folder, ok := h.folderParam(c)
	if !ok {
		return
	}
	uid, err := strconv.ParseUint(c.Param("uid"), 10, 32)
	if err != nil || uid == 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	partID := strings.TrimSpace(c.Param("part_id"))
	if partID == "" || len(partID) > 64 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	for _, r := range partID {
		if r < '0' || r > '9' {
			if r != '.' {
				authError(c, http.StatusBadRequest, "INVALID_PARAMS")
				return
			}
		}
	}
	client, _, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	if value, provided := c.GetQuery("uid_validity"); provided {
		expected, parseErr := strconv.ParseUint(value, 10, 32)
		if parseErr != nil || expected == 0 {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		if !mailboxExpectedValidity(c, client, folder, uint32(expected), true) {
			return
		}
	}
	filename, _, data, err := client.GetAttachment(c.Request.Context(), folder, uint32(uid), partID)
	if err != nil {
		if errors.Is(err, webmail.ErrAttachmentTooLarge) {
			authError(c, http.StatusRequestEntityTooLarge, "ATTACHMENT_TOO_LARGE")
			return
		}
		if errors.Is(err, webmail.ErrInvalidMIME) {
			authError(c, http.StatusUnprocessableEntity, "ATTACHMENT_INVALID")
			return
		}
		if strings.Contains(err.Error(), "not found") {
			authError(c, http.StatusNotFound, "ATTACHMENT_NOT_FOUND")
			return
		}
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	safeName := path.Base(strings.ReplaceAll(filename, "\\", "/"))
	if safeName == "." || safeName == "/" || safeName == "" {
		safeName = "attachment-" + partID
	}
	disp := mime.FormatMediaType("attachment", map[string]string{"filename": safeName})
	if disp == "" {
		disp = `attachment; filename="attachment-` + partID + `"`
	}
	c.Header("Content-Disposition", disp)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'none'; sandbox")
	c.Header("Cache-Control", "no-store")
	// Always octet-stream + nosniff (same posture as support/archive downloads).
	c.Data(http.StatusOK, "application/octet-stream", data)
}

type mailboxFlagsRequest struct {
	UIDValidity uint32   `json:"uid_validity"`
	UIDs        []uint32 `json:"uids"`
	Add         []string `json:"add"`
	Remove      []string `json:"remove"`
}

// FlagMessages adds/removes IMAP flags for UIDs in a folder.
// POST /mailbox/folders/:name/messages/flags
func (h *MailboxNativeHandler) FlagMessages(c *gin.Context) {
	folder, ok := h.folderParam(c)
	if !ok {
		return
	}
	var req mailboxFlagsRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.UIDs) == 0 || (len(req.Add) == 0 && len(req.Remove) == 0) {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	if len(req.UIDs) > webmail.MaxPageSize {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	add, remove := sanitizeFlags(req.Add), sanitizeFlags(req.Remove)
	if len(add) == 0 && len(remove) == 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	client, session, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	if !mailboxExpectedValidity(c, client, folder, req.UIDValidity, true) {
		return
	}
	deleting := false
	for _, flag := range add {
		if strings.EqualFold(flag, "\\Deleted") {
			deleting = true
		}
	}
	if deleting {
		release, guarded := h.guardDraftMutation(c, client, session, folder, req.UIDValidity, req.UIDs, false, nil)
		if !guarded {
			return
		}
		defer release()
	}
	if err := client.SetFlags(c.Request.Context(), folder, req.UIDs, add, remove); err != nil {
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": len(req.UIDs)})
}

type mailboxMoveRequest struct {
	UIDValidity uint32   `json:"uid_validity"`
	UIDs        []uint32 `json:"uids"`
	To          string   `json:"to"`
}

// MoveMessages moves UIDs to another folder (IMAP UID MOVE).
// POST /mailbox/folders/:name/messages/move
func (h *MailboxNativeHandler) MoveMessages(c *gin.Context) {
	folder, ok := h.folderParam(c)
	if !ok {
		return
	}
	var req mailboxMoveRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.UIDs) == 0 || strings.TrimSpace(req.To) == "" {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	if len(req.UIDs) > webmail.MaxPageSize {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	dest := strings.TrimSpace(req.To)
	if decoded, err := webmail.DecodeFolderName(dest); err == nil {
		dest = decoded
	}
	// Same guard as transferMessages: move targets must be managed folders and
	// different from the source, so IMAP-internal or protected names are rejected.
	if !webmail.ValidManagedFolder(dest) || dest == folder {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	client, session, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	if !mailboxExpectedValidity(c, client, folder, req.UIDValidity, true) {
		return
	}
	release, guarded := h.guardDraftMutation(c, client, session, folder, req.UIDValidity, req.UIDs, false, nil)
	if !guarded {
		return
	}
	defer release()
	destinations, err := client.MoveMessages(c.Request.Context(), folder, req.UIDs, dest)
	if err != nil {
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	// destination_uids enables exact undo on the client; a nil slice means the
	// server omitted COPYUID and the UI must not pretend it can move back.
	response := gin.H{"moved": len(req.UIDs), "to": dest}
	if destinations != nil {
		response["destination_uids"] = destinations
	}
	c.JSON(http.StatusOK, response)
}

type mailboxDeleteRequest struct {
	UIDValidity uint32   `json:"uid_validity"`
	UIDs        []uint32 `json:"uids"`
	Expunge     bool     `json:"expunge"`
}

// DeleteMessages marks UIDs \Deleted and optionally expunges.
// POST /mailbox/folders/:name/messages/delete
func (h *MailboxNativeHandler) DeleteMessages(c *gin.Context) {
	folder, ok := h.folderParam(c)
	if !ok {
		return
	}
	var req mailboxDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.UIDs) == 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	if len(req.UIDs) > webmail.MaxPageSize {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	client, session, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	if !mailboxExpectedValidity(c, client, folder, req.UIDValidity, true) {
		return
	}
	release, guarded := h.guardDraftMutation(c, client, session, folder, req.UIDValidity, req.UIDs, true, nil)
	if !guarded {
		return
	}
	defer release()
	if err := client.DeleteMessages(c.Request.Context(), folder, req.UIDs, req.Expunge); err != nil {
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": len(req.UIDs), "expunged": req.Expunge})
}

func sanitizeFlags(flags []string) []string {
	allowed := map[string]struct{}{
		`\Seen`: {}, `\Answered`: {}, `\Flagged`: {}, `\Deleted`: {}, `\Draft`: {},
		"$Forwarded": {},
	}
	var out []string
	for _, f := range flags {
		f = strings.TrimSpace(f)
		if _, ok := allowed[f]; ok {
			out = append(out, f)
		}
	}
	return out
}

// GetRawMessage streams the byte-faithful RFC822 payload of one message.
// GET /mailbox/folders/:name/messages/:uid/raw
func (h *MailboxNativeHandler) GetRawMessage(c *gin.Context) {
	folder, ok := h.folderParam(c)
	if !ok {
		return
	}
	uid, err := strconv.ParseUint(c.Param("uid"), 10, 32)
	if err != nil || uid == 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	client, _, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	if value, provided := c.GetQuery("uid_validity"); provided {
		expected, parseErr := strconv.ParseUint(value, 10, 32)
		if parseErr != nil || expected == 0 {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		if !mailboxExpectedValidity(c, client, folder, uint32(expected), true) {
			return
		}
	}
	accessor, capable := client.(webmail.RawMessageAccessor)
	if !capable {
		authError(c, http.StatusNotImplemented, "MAIL_RAW_EXPORT_UNSUPPORTED")
		return
	}
	raw, validity, err := accessor.GetRawMessage(c.Request.Context(), folder, uint32(uid))
	if err != nil {
		if errors.Is(err, webmail.ErrMessageNotFound) || strings.Contains(err.Error(), "not found") {
			authError(c, http.StatusNotFound, "MESSAGE_NOT_FOUND")
			return
		}
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	filename := "message-" + c.Param("uid") + ".eml"
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	c.Header("X-UIDValidity", strconv.FormatUint(uint64(validity), 10))
	c.Data(http.StatusOK, "message/rfc822", raw)
}

// ImportMessage refuses the legacy raw write without a reviewed durable receipt.
func (h *MailboxNativeHandler) ImportMessage(c *gin.Context) {
	authError(c, http.StatusConflict, "MAIL_IMPORT_PREVIEW_REQUIRED")
}

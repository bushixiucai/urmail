package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/bushixiucai/urmail/internal/webmail"
)

type mailboxFolderRequest struct {
	Name        string `json:"name"`
	UIDValidity uint32 `json:"uid_validity"`
}

func folderInList(folders []webmail.Folder, name string) (webmail.Folder, bool) {
	for _, folder := range folders {
		if folder.Name == name || strings.EqualFold(folder.Name, "INBOX") && strings.EqualFold(name, "INBOX") {
			return folder, true
		}
	}
	return webmail.Folder{}, false
}

func mailboxExpectedValidity(c *gin.Context, client webmail.Client, folder string, expected uint32, required bool) bool {
	if expected == 0 {
		if required {
			authError(c, 400, "INVALID_PARAMS")
			return false
		}
		return true
	}
	actual, err := client.FolderUIDValidity(c.Request.Context(), folder)
	if err != nil {
		authError(c, 502, "IMAP_UNAVAILABLE")
		return false
	}
	if actual != expected {
		authError(c, 409, "DRAFT_CHANGED")
		return false
	}
	return true
}

// CreateFolder creates a custom mailbox for the authenticated IMAP identity.
func (h *MailboxNativeHandler) CreateFolder(c *gin.Context) {
	var req mailboxFolderRequest
	if !bindMailboxMessage(c, &req) || !webmail.ValidManagedFolder(req.Name) || webmail.ProtectedFolder(webmail.Folder{Name: req.Name}) {
		if !c.IsAborted() {
			authError(c, 400, "INVALID_PARAMS")
		}
		return
	}
	client, _, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	manager, ok := client.(webmail.FolderManager)
	if !ok {
		authError(c, 503, "WEBMAIL_DISABLED")
		return
	}
	folders, err := client.ListFolders(c)
	if err != nil {
		authError(c, 502, "IMAP_UNAVAILABLE")
		return
	}
	if _, exists := folderInList(folders, req.Name); exists {
		authError(c, 409, "FOLDER_CONFLICT")
		return
	}
	if err = manager.CreateFolder(c.Request.Context(), req.Name); err != nil {
		authError(c, 502, "IMAP_UNAVAILABLE")
		return
	}
	validity, err := client.FolderUIDValidity(c.Request.Context(), req.Name)
	if err != nil {
		authError(c, 502, "IMAP_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"name": req.Name, "name_enc": webmail.EncodeFolderName(req.Name), "uid_validity": validity})
}

// RenameFolder changes only a custom mailbox, with a generation precondition.
func (h *MailboxNativeHandler) RenameFolder(c *gin.Context) { h.mutateFolder(c, false) }

// DeleteFolder recovers custom folders under Trash, retaining late arrivals.
func (h *MailboxNativeHandler) DeleteFolder(c *gin.Context) { h.mutateFolder(c, true) }

func (h *MailboxNativeHandler) mutateFolder(c *gin.Context, remove bool) {
	name, ok := h.folderParam(c)
	if !ok {
		return
	}
	var req mailboxFolderRequest
	if remove {
		value, err := strconv.ParseUint(c.Query("uid_validity"), 10, 32)
		if err != nil {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		req.UIDValidity = uint32(value)
	} else if !bindMailboxMessage(c, &req) {
		return
	}
	if !webmail.ValidManagedFolder(name) || !remove && (!webmail.ValidManagedFolder(req.Name) || webmail.ProtectedFolder(webmail.Folder{Name: req.Name})) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	client, _, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	manager, ok := client.(webmail.FolderManager)
	if !ok {
		authError(c, 503, "WEBMAIL_DISABLED")
		return
	}
	folders, err := client.ListFolders(c)
	if err != nil {
		authError(c, 502, "IMAP_UNAVAILABLE")
		return
	}
	source, exists := folderInList(folders, name)
	if !exists {
		authError(c, 404, "MESSAGE_NOT_FOUND")
		return
	}
	if webmail.ProtectedFolder(source) {
		authError(c, 403, "FOLDER_PROTECTED")
		return
	}
	if !mailboxExpectedValidity(c, client, name, req.UIDValidity, true) {
		return
	}
	destination := req.Name
	if remove {
		delimiter := source.Delimiter
		if delimiter == "" {
			delimiter = "/"
		}
		for _, folder := range folders {
			if strings.HasPrefix(folder.Name, name+delimiter) {
				authError(c, 409, "FOLDER_NOT_EMPTY")
				return
			}
		}
		contents, e := client.ListMessages(c.Request.Context(), name, webmail.ListOpts{Limit: 1})
		if e != nil {
			authError(c, 502, "IMAP_UNAVAILABLE")
			return
		}
		if contents.Total != 0 {
			authError(c, 409, "FOLDER_NOT_EMPTY")
			return
		}
		// IMAP has no atomic empty-and-DELETE. Rename into a recoverable
		// namespace so a concurrent late APPEND is retained with the folder.
		trash := webmail.ResolveTrashFolder(folders)
		if trash == "" {
			trash = webmail.TrashFolderName
			if err = manager.CreateFolder(c.Request.Context(), trash); err != nil {
				authError(c, 502, "IMAP_UNAVAILABLE")
				return
			}
		}
		if target, found := folderInList(folders, trash); found && target.Delimiter != "" {
			delimiter = target.Delimiter
		}
		destination = trash + delimiter + "Deleted-" + uuid.NewString()
		if !mailboxExpectedValidity(c, client, name, req.UIDValidity, true) {
			return
		}
		err = manager.RenameFolder(c.Request.Context(), name, destination)
	} else {
		if _, exists = folderInList(folders, req.Name); exists {
			authError(c, 409, "FOLDER_CONFLICT")
			return
		}
		err = manager.RenameFolder(c.Request.Context(), name, req.Name)
	}
	if err != nil {
		authError(c, 502, "IMAP_UNAVAILABLE")
		return
	}
	c.JSON(200, gin.H{"name": destination, "name_enc": webmail.EncodeFolderName(destination), "destination": destination, "deleted": remove, "recoverable": remove})
}

type mailboxTransferRequest struct {
	UIDs        []uint32 `json:"uids"`
	To          string   `json:"to"`
	UIDValidity uint32   `json:"uid_validity"`
}

// CopyMessages preserves originals and reports the server's UIDPLUS mapping.
func (h *MailboxNativeHandler) CopyMessages(c *gin.Context) { h.transferMessages(c, "copy") }

// TrashMessages moves messages into the resolved Trash without permanent loss.
func (h *MailboxNativeHandler) TrashMessages(c *gin.Context) { h.transferMessages(c, "trash") }

// RestoreMessages moves Trash messages to the user's selected destination.
func (h *MailboxNativeHandler) RestoreMessages(c *gin.Context) { h.transferMessages(c, "restore") }

func (h *MailboxNativeHandler) transferMessages(c *gin.Context, operation string) {
	if !h.requireEnabled(c) {
		return
	}
	folder, ok := h.folderParam(c)
	if !ok {
		return
	}
	var req mailboxTransferRequest
	if !bindMailboxMessage(c, &req) {
		return
	}
	if len(req.UIDs) == 0 || len(req.UIDs) > webmail.MaxPageSize || req.UIDValidity == 0 || !webmail.ValidManagedFolder(folder) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	seen := map[uint32]bool{}
	for _, uid := range req.UIDs {
		if uid == 0 || seen[uid] {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		seen[uid] = true
	}
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if len(key) < 8 || len(key) > 200 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	digest := transferDigest(operation, folder, req)
	if !h.transferJournal(c, key, digest, false) {
		return
	}
	client, _, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	manager, ok := client.(webmail.FolderManager)
	if !ok {
		authError(c, 503, "WEBMAIL_DISABLED")
		return
	}
	if !mailboxExpectedValidity(c, client, folder, req.UIDValidity, true) {
		return
	}
	folders, err := client.ListFolders(c)
	if err != nil {
		authError(c, 502, "IMAP_UNAVAILABLE")
		return
	}
	destination := req.To
	switch operation {
	case "trash":
		destination = webmail.ResolveTrashFolder(folders)
		if destination == "" {
			destination = webmail.TrashFolderName
			if err = manager.CreateFolder(c.Request.Context(), destination); err != nil {
				authError(c, 502, "IMAP_UNAVAILABLE")
				return
			}
		}
	case "restore":
		if folder != webmail.ResolveTrashFolder(folders) {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		if destination == "" {
			destination = "INBOX"
		}
	}
	if strings.HasPrefix(destination, "b64.") {
		destination, err = webmail.DecodeFolderName(destination)
		if err != nil {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
	}
	if !webmail.ValidManagedFolder(destination) || destination == folder {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if operation != "trash" {
		target, exists := folderInList(folders, destination)
		if !exists {
			authError(c, 404, "MESSAGE_NOT_FOUND")
			return
		}
		for _, flag := range target.Flags {
			if strings.EqualFold(flag, `\Noselect`) {
				authError(c, 400, "INVALID_PARAMS")
				return
			}
		}
	}
	if !h.transferJournal(c, key, digest, true) {
		return
	}
	result, err := manager.TransferMessages(c.Request.Context(), folder, req.UIDs, destination, operation != "copy")
	certain := err == nil
	for _, item := range result.Results {
		if item.Status == "unknown" {
			certain = false
		}
	}
	if !h.finishTransferJournal(c, key, digest, result, certain) {
		certain = false
	}

	if !certain {
		c.JSON(202, gin.H{"results": result.Results, "destination": destination, "name_enc": result.NameEnc, "uid_validity": result.UIDValidity, "atomic": false, "uncertain": true})
		return
	}
	c.JSON(200, result)
}

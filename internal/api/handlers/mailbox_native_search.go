package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/webmail"
)

// CrossFolderSearch fans a query out across folders and merges the hits by
// date. GET /mailbox/search
//
// Query params:
//   - q — same syntax as the per-folder search (webmail.BuildSearchCriteria);
//     required non-empty (400 otherwise).
//   - folders — optional comma-separated folder names or b64.-encoded
//     name_enc values; omitted means all top-level folders (bounded by
//     webmail.MaxSearchFolders, \Noselect skipped).
//   - limit — per-folder hit cap, clamped to webmail.MaxSearchPerFolder.
//
// The whole fan-out shares one request deadline (webmail.DefaultSearchDeadline
// when the caller set none); folders that fail are skipped (best effort).
// Every hit is tagged with folder/folder_enc so the client can jump into the
// source mailbox.
func (h *MailboxNativeHandler) CrossFolderSearch(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	if q == "" {
		authError(c, http.StatusBadRequest, "INVALID_SEARCH_QUERY")
		return
	}
	if _, err := webmail.BuildSearchCriteria(q); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_SEARCH_QUERY")
		return
	}
	var folders []string
	if raw := strings.TrimSpace(c.Query("folders")); raw != "" {
		for _, name := range strings.Split(raw, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if decoded, err := webmail.DecodeFolderName(name); err == nil {
				name = decoded
			}
			folders = append(folders, name)
		}
	}
	perFolder, _ := strconv.Atoi(c.Query("limit"))
	client, _, ok := h.dialIMAP(c)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()
	lister, capable := client.(webmail.FolderLister)
	if !capable {
		authError(c, http.StatusNotImplemented, "IMAP_UNAVAILABLE")
		return
	}
	merged, err := webmail.SearchAll(c.Request.Context(), lister, q, folders, perFolder)
	if err != nil {
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	if merged == nil {
		merged = []webmail.MessageSummary{}
	}
	c.JSON(http.StatusOK, gin.H{
		"messages": merged,
		"total":    len(merged),
		"q":        q,
	})
}

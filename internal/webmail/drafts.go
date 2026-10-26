package webmail

import (
	"strings"
)

// DraftsFolderName is the default mailbox created when no SPECIAL-USE \Drafts
// folder exists yet. Dovecot namespace maps this name to special_use=\Drafts.
const DraftsFolderName = "Drafts"

// ResolveDraftsFolder picks the IMAP Drafts mailbox from a LIST result.
//
// Rules (first match wins):
//  1. Any folder whose flags include SPECIAL-USE \Drafts (case-insensitive)
//  2. Any folder whose name equals "Drafts" (case-insensitive)
//
// Returns "" when neither is present (caller should CREATE DraftsFolderName).
func ResolveDraftsFolder(folders []Folder) string {
	for _, f := range folders {
		if folderHasFlag(f.Flags, `\Drafts`) {
			return f.Name
		}
	}
	for _, f := range folders {
		if strings.EqualFold(strings.TrimSpace(f.Name), DraftsFolderName) {
			return f.Name
		}
	}
	return ""
}

// IsDraftsFolder reports whether a LIST entry is the drafts mailbox.
func IsDraftsFolder(f Folder) bool {
	if folderHasFlag(f.Flags, `\Drafts`) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(f.Name), DraftsFolderName)
}

func folderHasFlag(flags []string, want string) bool {
	want = strings.ToLower(want)
	for _, f := range flags {
		if strings.ToLower(f) == want {
			return true
		}
	}
	return false
}

// DraftSaveResult is returned after APPEND (+ optional replace delete).
type DraftSaveResult struct {
	Folder         string `json:"folder"`
	NameEnc        string `json:"name_enc"`
	UID            uint32 `json:"uid"`
	UIDValidity    uint32 `json:"uid_validity"`
	CleanupPending bool   `json:"cleanup_pending,omitempty"`
}

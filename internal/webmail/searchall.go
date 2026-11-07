package webmail

import (
	"context"
	"sort"
	"strings"
	"time"
)

// Cross-folder search bounds. Bounded fan-out keeps a single request from
// walking a huge namespace or pinning the one IMAP connection forever.
const (
	// MaxSearchFolders caps how many folders one fan-out request visits.
	MaxSearchFolders = 20
	// MaxSearchPerFolder caps fetched hits per folder.
	MaxSearchPerFolder = 50
	// MaxSearchTotal caps the merged result set.
	MaxSearchTotal = 200
	// DefaultSearchDeadline bounds the whole fan-out when the caller set none.
	DefaultSearchDeadline = 8 * time.Second
)

// FolderLister is the slice of Client the fan-out needs; fakes in tests
// implement exactly this.
type FolderLister interface {
	ListFolders(ctx context.Context) ([]Folder, error)
	ListMessages(ctx context.Context, folder string, opts ListOpts) (ListResult, error)
}

// SearchAll fans a query out across folders and merges results by date
// (descending). Scope rules:
//
//   - folders nil/empty: every TOP-LEVEL folder (no hierarchy delimiter in
//     the name, skipping \Noselect containers), capped at MaxSearchFolders;
//   - folders named: used as-is (already decoded), capped likewise.
//
// Each folder gets one ListMessages call with Query=q and Limit=perFolder;
// the whole fan-out shares ctx (handlers pass a deadline-bounded context).
// Per-folder errors are skipped (best effort) so one broken mailbox does not
// hide the rest; the merged list is trimmed to MaxSearchTotal.
func SearchAll(ctx context.Context, client FolderLister, q string, folders []string, perFolder int) ([]MessageSummary, error) {
	if perFolder <= 0 || perFolder > MaxSearchPerFolder {
		perFolder = MaxSearchPerFolder
	}
	names, err := resolveSearchFolders(ctx, client, folders)
	if err != nil {
		return nil, err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultSearchDeadline)
		defer cancel()
	}
	var merged []MessageSummary
	for _, name := range names {
		if ctx.Err() != nil {
			break
		}
		res, listErr := client.ListMessages(ctx, name, ListOpts{Limit: perFolder, Query: q})
		if listErr != nil || ctx.Err() != nil {
			continue // best-effort per folder
		}
		for _, m := range res.Messages {
			m.UIDValidity = res.UIDValidity
			m.Folder = name
			m.FolderEnc = EncodeFolderName(name)
			merged = append(merged, m)
		}
	}
	sort.SliceStable(merged, func(i, j int) bool { return summaryDate(merged[i]).After(summaryDate(merged[j])) })
	if len(merged) > MaxSearchTotal {
		merged = merged[:MaxSearchTotal]
	}
	return merged, nil
}

// resolveSearchFolders maps the requested scope to concrete mailbox names.
func resolveSearchFolders(ctx context.Context, client FolderLister, folders []string) ([]string, error) {
	if len(folders) == 0 {
		list, err := client.ListFolders(ctx)
		if err != nil {
			return nil, err
		}
		var top []string
		for _, f := range list {
			if hasFlagFolded(f.Flags, "\\Noselect") {
				continue
			}
			if f.Delimiter != "" && strings.Contains(f.Name, f.Delimiter) {
				continue // only top-level
			}
			top = append(top, f.Name)
		}
		folders = top
	}
	if len(folders) > MaxSearchFolders {
		folders = folders[:MaxSearchFolders]
	}
	return folders, nil
}

func hasFlagFolded(flags []string, want string) bool {
	want = strings.ToLower(want)
	for _, f := range flags {
		if strings.ToLower(strings.TrimSpace(f)) == want {
			return true
		}
	}
	return false
}

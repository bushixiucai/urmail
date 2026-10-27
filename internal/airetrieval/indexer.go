package airetrieval

import (
	"context"
	"sort"
)

// Indexer refreshes one mailbox's retrieval index incrementally. A message
// whose indexed content digest is unchanged is skipped; deleted or expunged
// messages and vanished folders, or folders whose UIDVALIDITY changed, are
// removed before anything new is written.
type Indexer struct {
	Store  ChunkStore
	Source MailSource
}

// Reindex synchronizes the index with the mailbox's live messages and returns
// how many messages were (re-)chunked in this pass.
func (i Indexer) Reindex(ctx context.Context, p Principal) (int, error) {
	if !p.Valid() {
		return 0, ErrNoPrincipal
	}
	if i.Store == nil || i.Source == nil {
		return 0, ErrNoSource
	}
	states, err := i.Store.MessageStates(ctx, p)
	if err != nil {
		return 0, err
	}
	folders, err := i.Source.Folders(ctx)
	if err != nil {
		return 0, err
	}
	liveFolders := make(map[string]bool, len(folders))
	reindexed := 0
	for _, folder := range folders {
		if folder == "" {
			continue
		}
		liveFolders[folder] = true
		uidValidity, uids, err := i.Source.MessageUIDs(ctx, folder)
		if err != nil {
			return reindexed, err
		}
		// A changed UIDVALIDITY invalidates every stored UID of the folder.
		staleValidity := false
		for key := range states {
			if key.Folder == folder {
				if key.UIDValidity != uidValidity {
					staleValidity = true
					break
				}
			}
		}
		if staleValidity {
			if err := i.Store.DeleteFolder(ctx, p, folder); err != nil {
				return reindexed, err
			}
			for key := range states {
				if key.Folder == folder {
					delete(states, key)
				}
			}
		}
		live := make(map[uint32]bool, len(uids))
		for _, uid := range uids {
			live[uid] = true
			key := MessageKey{Folder: folder, UIDValidity: uidValidity, UID: uid}
			text, err := i.Source.MessageText(ctx, folder, uid)
			if err != nil {
				return reindexed, err
			}
			digest := ContentDigest(text)
			if state, ok := states[key]; ok && state.Digest == digest {
				continue // incremental: content unchanged, keep stored chunks
			}
			version := 1
			if state, ok := states[key]; ok {
				version = state.Version + 1
			}
			contents := ChunkText(text)
			if len(contents) == 0 {
				if err := i.Store.DeleteMessages(ctx, p, folder, uidValidity, []uint32{uid}); err != nil {
					return reindexed, err
				}
				continue
			}
			if err := i.Store.ReplaceMessage(ctx, p, folder, uidValidity, uid, version, digest, contents); err != nil {
				return reindexed, err
			}
			reindexed++
		}
		// Expunged messages drop out immediately.
		var gone []uint32
		for key := range states {
			if key.Folder == folder && key.UIDValidity == uidValidity && !live[key.UID] {
				gone = append(gone, key.UID)
			}
		}
		sort.Slice(gone, func(a, b int) bool { return gone[a] < gone[b] })
		if len(gone) > 0 {
			if err := i.Store.DeleteMessages(ctx, p, folder, uidValidity, gone); err != nil {
				return reindexed, err
			}
		}
	}
	// Whole folders that disappeared drop out.
	for key := range states {
		if !liveFolders[key.Folder] {
			if err := i.Store.DeleteFolder(ctx, p, key.Folder); err != nil {
				return reindexed, err
			}
			delete(states, key)
		}
	}
	return reindexed, nil
}

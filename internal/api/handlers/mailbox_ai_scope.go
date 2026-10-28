package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/bushixiucai/urmail/internal/airetrieval"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// askScopeSource narrows retrieval and final citation checks without modifying the canonical index.
type askScopeSource struct {
	base          airetrieval.MailSource
	folder        string
	after, before time.Time
	client        webmail.Client
}

func (s askScopeSource) Folders(ctx context.Context) ([]string, error) {
	folders, err := s.base.Folders(ctx)
	if err != nil {
		return nil, err
	}
	if s.folder == "" {
		return folders, nil
	}
	for _, folder := range folders {
		if folder == s.folder {
			return []string{folder}, nil
		}
	}
	return nil, nil
}
func (s askScopeSource) MessageUIDs(ctx context.Context, folder string) (uint32, []uint32, error) {
	if s.folder != "" && folder != s.folder {
		return 0, nil, nil
	}
	validity, uids, err := s.base.MessageUIDs(ctx, folder)
	if err != nil || s.after.IsZero() && s.before.IsZero() {
		return validity, uids, err
	}
	if s.client == nil {
		return 0, nil, errors.New("AI date filtering requires mailbox metadata")
	}
	allowed := map[uint32]bool{}
	var cursor uint32
	for pages := 0; pages < 1000; pages++ {
		result, err := s.client.ListMessages(ctx, folder, webmail.ListOpts{Limit: webmail.MaxPageSize, CursorUID: cursor})
		if err != nil {
			return 0, nil, err
		}
		for _, message := range result.Messages {
			if (s.after.IsZero() || !message.Date.Before(s.after)) && (s.before.IsZero() || message.Date.Before(s.before)) {
				allowed[message.UID] = true
			}
		}
		if result.NextCursor == 0 {
			out := make([]uint32, 0, len(uids))
			for _, uid := range uids {
				if allowed[uid] {
					out = append(out, uid)
				}
			}
			return validity, out, nil
		}
		if cursor == result.NextCursor {
			return 0, nil, errors.New("AI metadata pagination did not advance")
		}
		cursor = result.NextCursor
	}
	return 0, nil, errors.New("AI metadata budget exceeded")
}
func (s askScopeSource) MessageText(ctx context.Context, folder string, uid uint32) (string, error) {
	return s.base.MessageText(ctx, folder, uid)
}

// askScopeStore applies filters before the candidate limit. Other methods retain
// canonical ownership checks and stable citation identities.
type askScopeStore struct {
	airetrieval.ChunkStore
	source askScopeSource
}

func (s askScopeStore) SearchChunks(ctx context.Context, p airetrieval.Principal, query string, limit int) ([]airetrieval.Chunk, error) {
	folders, err := s.source.Folders(ctx)
	if err != nil {
		return nil, err
	}
	scope := make([]airetrieval.SearchScope, 0, len(folders))
	allowed := map[airetrieval.MessageKey]bool{}
	for _, folder := range folders {
		validity, uids, err := s.source.MessageUIDs(ctx, folder)
		if err != nil {
			return nil, err
		}
		scope = append(scope, airetrieval.SearchScope{Folder: folder, Validity: validity, UIDs: uids})
		for _, uid := range uids {
			allowed[airetrieval.MessageKey{Folder: folder, UIDValidity: validity, UID: uid}] = true
		}
	}
	if pg, ok := s.ChunkStore.(interface {
		SearchChunksInScope(context.Context, airetrieval.Principal, string, int, []airetrieval.SearchScope) ([]airetrieval.Chunk, error)
	}); ok {
		return pg.SearchChunksInScope(ctx, p, query, limit, scope)
	}
	// Offline stores have no SQL filtering; explicitly reject an exhausted scan
	// rather than silently dropping eligible later candidates.
	chunks, err := s.ChunkStore.SearchChunks(ctx, p, query, 100001)
	if err != nil {
		return nil, err
	}
	if len(chunks) > 100000 {
		return nil, errors.New("AI scoped candidate budget exceeded")
	}
	out := make([]airetrieval.Chunk, 0, limit)
	for _, chunk := range chunks {
		if allowed[airetrieval.MessageKey{Folder: chunk.Folder, UIDValidity: chunk.UIDValidity, UID: chunk.UID}] {
			out = append(out, chunk)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

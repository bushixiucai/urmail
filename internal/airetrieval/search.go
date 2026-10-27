package airetrieval

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// Errors reported by the retrieval pipeline.
var (
	ErrNoPrincipal = errors.New("airetrieval: missing mailbox principal")
	ErrNoSource    = errors.New("airetrieval: missing mail source or store")
	ErrInvalidAsk  = errors.New("airetrieval: invalid ask request")
)

const maxSearchHits = 20

// Search returns permission-filtered chunks for the principal. Every access
// point re-verifies authorization:
//
//  1. the store query is tenant/mailbox scoped (and RLS fail-closes without
//     the mailbox context);
//  2. each hit is re-read by id under the same principal (a stale cache or a
//     chunk that moved to another mailbox yields not-found and is dropped);
//  3. the source message must still be live (listed under the folder's
//     current UIDVALIDITY), so deleted or expunged mail never comes back.
//
// A revoked or deleted mailbox produces zero results because neither the
// store rows nor the live listing survive.
func Search(ctx context.Context, store ChunkStore, source MailSource, p Principal, query string) ([]Chunk, error) {
	if !p.Valid() {
		return nil, ErrNoPrincipal
	}
	if store == nil || source == nil {
		return nil, ErrNoSource
	}
	needle := strings.TrimSpace(query)
	if needle == "" {
		return nil, nil
	}
	candidates, err := store.SearchChunks(ctx, p, needle, maxSearchHits)
	if err != nil {
		return nil, err
	}
	liveFolders := map[string]map[uint32]bool{}
	var verified []Chunk
	for _, candidate := range candidates {
		// (2) re-read under the principal: catches cross-mailbox drift.
		chunk, found, err := store.GetChunk(ctx, p, candidate.ID)
		if err != nil {
			return nil, err
		}
		if !found || chunk.Folder != candidate.Folder || chunk.UID != candidate.UID || chunk.UIDValidity != candidate.UIDValidity {
			continue
		}
		// (3) message still readable in the live mailbox.
		live, err := liveUIDs(ctx, source, liveFolders, chunk.Folder)
		if err != nil {
			return nil, err
		}
		if !live[chunk.UID] {
			continue
		}
		verified = append(verified, chunk)
	}
	return verified, nil
}

func liveUIDs(ctx context.Context, source MailSource, cache map[string]map[uint32]bool, folder string) (map[uint32]bool, error) {
	if live, ok := cache[folder]; ok {
		return live, nil
	}
	uidValidity, uids, err := source.MessageUIDs(ctx, folder)
	if err != nil {
		return nil, err
	}
	live := make(map[uint32]bool, len(uids))
	for _, uid := range uids {
		live[uid] = true
	}
	cache[folder+"\x00"+strconv.FormatUint(uint64(uidValidity), 10)] = live
	return live, nil
}

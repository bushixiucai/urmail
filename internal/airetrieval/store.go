package airetrieval

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

// ChunkStore persists and reads retrieval chunks for one mailbox principal.
// Implementations must enforce the principal on every method: the PG
// implementation repeats the tenant/mailbox predicates in every statement and
// the mailbox_ai_chunks RLS policy fail-closes without the mailbox context.
type ChunkStore interface {
	MessageStates(ctx context.Context, p Principal) (map[MessageKey]MessageState, error)
	ReplaceMessage(ctx context.Context, p Principal, folder string, uidValidity, uid uint32, version int, digest string, contents []string) error
	DeleteMessages(ctx context.Context, p Principal, folder string, uidValidity uint32, uids []uint32) error
	DeleteFolder(ctx context.Context, p Principal, folder string) error
	SearchChunks(ctx context.Context, p Principal, query string, limit int) ([]Chunk, error)
	// GetChunk re-reads one chunk under the given principal. found=false is
	// returned when the chunk does not belong to this principal anymore.
	GetChunk(ctx context.Context, p Principal, id int64) (Chunk, bool, error)
}

// PGChunkStore implements ChunkStore on the shared PostgreSQL database.
type PGChunkStore struct{ Pool *db.Pool }

// LoadChunkStore returns the platform-scoped queries for one transaction.
type txOp func(ctx context.Context, q *store.Queries) error

func (s PGChunkStore) inTx(ctx context.Context, p Principal, op txOp) error {
	if s.Pool == nil || !p.Valid() {
		return errors.New("airetrieval: missing store pool or principal")
	}
	tx, err := s.Pool.MailboxTx(ctx, p.TenantID, p.MailboxID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := op(ctx, store.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MessageStates reads the indexed digest and content version per message.
func (s PGChunkStore) MessageStates(ctx context.Context, p Principal) (map[MessageKey]MessageState, error) {
	var out map[MessageKey]MessageState
	err := s.inTx(ctx, p, func(ctx context.Context, q *store.Queries) error {
		rows, err := q.MailboxAIChunkMessageStates(ctx, store.MailboxAIChunkMessageStatesParams{TenantID: p.TenantID, MailboxID: p.MailboxID})
		if err != nil {
			return err
		}
		out = make(map[MessageKey]MessageState, len(rows))
		for _, row := range rows {
			if row.FolderUidvalidity < 0 || row.FolderUidvalidity > math.MaxUint32 || row.MessageUid < 0 || row.MessageUid > math.MaxUint32 {
				continue // corrupt row outside the uint32 uid space; skip
			}
			out[MessageKey{Folder: row.Folder, UIDValidity: uint32(row.FolderUidvalidity), UID: uint32(row.MessageUid)}] = MessageState{
				Digest:  row.ContentDigest,
				Version: int(row.ContentVersion),
			}
		}
		return nil
	})
	return out, err
}

// ReplaceMessage atomically replaces every chunk of one indexed message.
func (s PGChunkStore) ReplaceMessage(ctx context.Context, p Principal, folder string, uidValidity, uid uint32, version int, digest string, contents []string) error {
	return s.inTx(ctx, p, func(ctx context.Context, q *store.Queries) error {
		if version < math.MinInt32 || version > math.MaxInt32 || len(contents) > math.MaxInt32 {
			return fmt.Errorf("airetrieval: message version or chunk count out of range")
		}
		if _, err := q.DeleteMailboxAIChunkMessages(ctx, store.DeleteMailboxAIChunkMessagesParams{
			TenantID: p.TenantID, MailboxID: p.MailboxID, Folder: folder,
			FolderUidvalidity: int64(uidValidity), Column5: []int64{int64(uid)},
		}); err != nil {
			return err
		}
		for i, content := range contents {
			id, err := q.InsertMailboxAIChunk(ctx, store.InsertMailboxAIChunkParams{
				TenantID: p.TenantID, MailboxID: p.MailboxID, Folder: folder,
				FolderUidvalidity: int64(uidValidity), MessageUid: int64(uid),
				ChunkIndex: int32(i), ContentVersion: int32(version),
				Content: content, ContentDigest: digest,
			})
			if err != nil {
				return err
			}
			_ = id
		}
		return nil
	})
}

// DeleteMessages removes chunks of messages that left the folder.
func (s PGChunkStore) DeleteMessages(ctx context.Context, p Principal, folder string, uidValidity uint32, uids []uint32) error {
	if len(uids) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(uids))
	for _, uid := range uids {
		ids = append(ids, int64(uid))
	}
	return s.inTx(ctx, p, func(ctx context.Context, q *store.Queries) error {
		_, err := q.DeleteMailboxAIChunkMessages(ctx, store.DeleteMailboxAIChunkMessagesParams{
			TenantID: p.TenantID, MailboxID: p.MailboxID, Folder: folder,
			FolderUidvalidity: int64(uidValidity), Column5: ids,
		})
		return err
	})
}

// DeleteFolder drops a folder's whole indexed state (gone folder or new
// UIDVALIDITY).
func (s PGChunkStore) DeleteFolder(ctx context.Context, p Principal, folder string) error {
	return s.inTx(ctx, p, func(ctx context.Context, q *store.Queries) error {
		_, err := q.DeleteMailboxAIChunkFolder(ctx, store.DeleteMailboxAIChunkFolderParams{
			TenantID: p.TenantID, MailboxID: p.MailboxID, Folder: folder,
		})
		return err
	})
}

// SearchChunks returns candidate chunks matching the query. The statement is
// principal-scoped, and Search re-verifies every hit before use.
func (s PGChunkStore) SearchChunks(ctx context.Context, p Principal, query string, limit int) ([]Chunk, error) {
	var out []Chunk
	err := s.inTx(ctx, p, func(ctx context.Context, q *store.Queries) error {
		if limit < 0 || limit > math.MaxInt32 {
			return fmt.Errorf("airetrieval: limit out of range")
		}
		rows, err := q.SearchMailboxAIChunks(ctx, store.SearchMailboxAIChunksParams{
			TenantID: p.TenantID, MailboxID: p.MailboxID, Column3: query, Column4: int32(limit),
		})
		if err != nil {
			return err
		}
		out = make([]Chunk, 0, len(rows))
		for _, row := range rows {
			out = append(out, chunkFromRow(row.ID, row.Folder, row.FolderUidvalidity, row.MessageUid, row.ChunkIndex, row.ContentVersion, row.Content, row.ContentDigest))
		}
		return nil
	})
	return out, err
}

// GetChunk re-reads one chunk under the principal; a chunk of another mailbox
// (or a revoked/deleted mailbox) is reported as not found.
func (s PGChunkStore) GetChunk(ctx context.Context, p Principal, id int64) (Chunk, bool, error) {
	var found Chunk
	err := s.inTx(ctx, p, func(ctx context.Context, q *store.Queries) error {
		row, err := q.GetMailboxAIChunk(ctx, store.GetMailboxAIChunkParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = chunkFromRow(row.ID, row.Folder, row.FolderUidvalidity, row.MessageUid, row.ChunkIndex, row.ContentVersion, row.Content, row.ContentDigest)
		return nil
	})
	if err != nil {
		return Chunk{}, false, err
	}
	if found.ID == 0 {
		return Chunk{}, false, nil
	}
	return found, true, nil
}

func chunkFromRow(id int64, folder string, uidValidity, uid int64, chunkIndex, contentVersion int32, content, digest string) Chunk {
	return Chunk{
		ID: id, Folder: folder,
		// Columns round-trip uint32 values stored via int64(uidValidity);
		// writers are uint32, so the conversion cannot overflow.
		UIDValidity: uint32(uidValidity), //nolint:gosec // uint32 written as int64, round-trip safe
		UID:         uint32(uid),         //nolint:gosec // uint32 written as int64, round-trip safe
		ChunkIndex:  int(chunkIndex), ContentVersion: int(contentVersion),
		Content: content, Digest: digest,
	}
}

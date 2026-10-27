package eas

// Sync state persistence. Every statement runs inside a MailboxTx and names
// tenant_id and mailbox_id explicitly, matching the RLS policy pattern from
// migrations 0084/0118/0119. Cross-mailbox reads therefore fail closed.

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
)

// collectionState is the stored per-device, per-folder sync key state.
type collectionState struct {
	Epoch       int64
	ChangeSeq   int64
	UIDValidity int64
}

// loadCollectionState returns the stored state or ErrNotFound.
func loadCollectionState(ctx context.Context, pool *db.Pool, sess Session, deviceID, folderID string) (collectionState, error) {
	tx, err := pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if err != nil {
		return collectionState{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var s collectionState
	err = tx.QueryRow(ctx,
		`SELECT sync_epoch, change_seq, uid_validity
			  FROM mailbox_eas_collection_state
			 WHERE tenant_id = $1 AND mailbox_id = $2 AND device_id = $3 AND folder_id = $4`,
		sess.TenantID, sess.MailboxID, deviceID, folderID).Scan(&s.Epoch, &s.ChangeSeq, &s.UIDValidity)
	if errors.Is(err, pgx.ErrNoRows) {
		return collectionState{}, ErrNotFound
	}
	return s, err
}

// saveCollectionState upserts the state row inside one transaction.
func saveCollectionState(ctx context.Context, pool *db.Pool, sess Session, deviceID, folderID string, s collectionState) error {
	tx, err := pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = saveCollectionStateTx(ctx, tx, sess, deviceID, folderID, s); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func saveCollectionStateTx(ctx context.Context, tx pgx.Tx, sess Session, deviceID, folderID string, s collectionState) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO mailbox_eas_collection_state
			   (tenant_id, mailbox_id, device_id, folder_id, sync_epoch, change_seq, uid_validity)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)
			 ON CONFLICT (tenant_id, mailbox_id, device_id, folder_id) DO UPDATE SET
			   sync_epoch = EXCLUDED.sync_epoch,
			   change_seq = EXCLUDED.change_seq,
			   uid_validity = EXCLUDED.uid_validity,
			   updated_at = now()`,
		sess.TenantID, sess.MailboxID, deviceID, folderID, s.Epoch, s.ChangeSeq, s.UIDValidity)
	return err
}

// itemRow is one stored per-item snapshot entry.
type itemRow struct {
	ItemID      string
	Fingerprint string
}

// loadItems lists stored item snapshots for a collection.
func loadItems(ctx context.Context, pool *db.Pool, sess Session, deviceID, folderID string) (map[string]itemRow, error) {
	tx, err := pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx,
		`SELECT item_id, fingerprint FROM mailbox_eas_item_state
		  WHERE tenant_id = $1 AND mailbox_id = $2 AND device_id = $3 AND folder_id = $4`,
		sess.TenantID, sess.MailboxID, deviceID, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]itemRow{}
	for rows.Next() {
		var r itemRow
		if err = rows.Scan(&r.ItemID, &r.Fingerprint); err != nil {
			return nil, err
		}
		out[r.ItemID] = r
	}
	return out, rows.Err()
}

// replaceItemsTx rewrites the stored item snapshot inside the caller's
// transaction. emittedOnly means: delete nothing outside newItems, so items
// not yet sent stay queued for the next window.
func replaceItemsTx(ctx context.Context, tx pgx.Tx, sess Session, deviceID, folderID string, items map[string]itemRow) error {
	if _, err := tx.Exec(ctx,
		`DELETE FROM mailbox_eas_item_state
		  WHERE tenant_id = $1 AND mailbox_id = $2 AND device_id = $3 AND folder_id = $4`,
		sess.TenantID, sess.MailboxID, deviceID, folderID); err != nil {
		return err
	}
	for id, r := range items {
		if _, err := tx.Exec(ctx,
			`INSERT INTO mailbox_eas_item_state
			       (tenant_id, mailbox_id, device_id, folder_id, item_id, fingerprint)
			     VALUES ($1, $2, $3, $4, $5, $6)`,
			sess.TenantID, sess.MailboxID, deviceID, folderID, id, r.Fingerprint); err != nil {
			return err
		}
	}
	return nil
}

// deleteItemsTx removes the given item ids (client deletes, tombstones).
func deleteItemsTx(ctx context.Context, tx pgx.Tx, sess Session, deviceID, folderID string, ids []string) error {
	for _, id := range ids {
		if _, err := tx.Exec(ctx,
			`DELETE FROM mailbox_eas_item_state
			  WHERE tenant_id = $1 AND mailbox_id = $2 AND device_id = $3 AND folder_id = $4 AND item_id = $5`,
			sess.TenantID, sess.MailboxID, deviceID, folderID, id); err != nil {
			return err
		}
	}
	return nil
}

// changeRow is one mailbox_changes entry.
type changeRow struct {
	ID         int64
	ResourceID int64
	Version    int64
	Deleted    bool
}

// listChangesFor is the concrete change-log reader. resourceType is one of
// "contact" or "event".
func listChangesFor(ctx context.Context, pool *db.Pool, sess Session, afterID int64, resourceType string, limit int64) ([]changeRow, error) {
	tx, err := pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx,
		`SELECT id, resource_id, version, deleted FROM mailbox_changes
		  WHERE tenant_id = $1 AND mailbox_id = $2 AND id > $3 AND resource_type = $4
		  ORDER BY id LIMIT $5`,
		sess.TenantID, sess.MailboxID, afterID, resourceType, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []changeRow
	for rows.Next() {
		var r changeRow
		if err = rows.Scan(&r.ID, &r.ResourceID, &r.Version, &r.Deleted); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// formatSyncKey renders the client-visible key: epoch-sequence.
func formatSyncKey(epoch, seq int64) string {
	return strconv.FormatInt(epoch, 10) + "-" + strconv.FormatInt(seq, 10)
}

// parseSyncKey decodes a client key. ok is false for key 0 (full sync) or
// malformed values.
func parseSyncKey(key string) (epoch, seq int64, fullSync bool, ok bool) {
	if key == "0" {
		return 0, 0, true, true
	}
	epochStr, seqStr, found := strings.Cut(key, "-")
	if !found {
		return 0, 0, false, false
	}
	e, err := strconv.ParseInt(epochStr, 10, 64)
	if err != nil || e < 1 {
		return 0, 0, false, false
	}
	s, err := strconv.ParseInt(seqStr, 10, 64)
	if err != nil || s < 0 {
		return 0, 0, false, false
	}
	return e, s, false, true
}

package eas

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/store"
)

var errCollabClientIDConflict = errors.New("EAS ClientId already names a different write")

type collabAddResult struct {
	ServerID string `json:"server_id"`
	Version  int64  `json:"version"`
}
type collabAddRecord struct {
	Digest     string          `json:"digest"`
	Collection string          `json:"collection"`
	Result     collabAddResult `json:"result"`
}

// collabAddReceipt must run in the caller's mailbox transaction. The resource
// write and completed receipt commit together. Device identity and collection
// are bound into the durable key; request bytes bind replay semantics.
func collabAddReceipt(ctx context.Context, q *store.Queries, req *SyncRequest, collection, kind, clientID string, canonical []byte, write func() (collabAddResult, error)) (collabAddResult, error) {
	if (kind != "eas_contact_add" && kind != "eas_calendar_add") || strings.TrimSpace(clientID) == "" || len(clientID) > 256 {
		return collabAddResult{}, errCollabClientIDConflict
	}
	keyHash := sha256.Sum256([]byte(req.DeviceID + "\x00" + collection + "\x00" + clientID))
	key := hex.EncodeToString(keyHash[:])
	hash := sha256.Sum256(canonical)
	digest := hex.EncodeToString(hash[:])
	if err := q.PlatformLockDataImport(ctx, store.PlatformLockDataImportParams{TenantID: strconv.FormatInt(req.Session.TenantID, 10), MailboxID: strconv.FormatInt(req.Session.MailboxID, 10), IdempotencyKey: kind + ":" + key}); err != nil {
		return collabAddResult{}, err
	}
	row, err := q.PlatformGetDataImportReceipt(ctx, store.PlatformGetDataImportReceiptParams{TenantID: req.Session.TenantID, MailboxID: req.Session.MailboxID, Kind: kind, IdempotencyKey: key})
	if err == nil {
		var record collabAddRecord
		if row.Status != "done" || json.Unmarshal(row.Payload, &record) != nil || record.Digest != digest || record.Collection != collection || record.Result.ServerID == "" {
			return collabAddResult{}, errCollabClientIDConflict
		}
		return record.Result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return collabAddResult{}, err
	}
	result, err := write()
	if err != nil {
		return result, err
	}
	payload, err := json.Marshal(collabAddRecord{Digest: digest, Collection: collection, Result: result})
	if err != nil {
		return collabAddResult{}, err
	}
	err = q.PlatformInsertDataImportReceipt(ctx, store.PlatformInsertDataImportReceiptParams{TenantID: req.Session.TenantID, MailboxID: req.Session.MailboxID, Kind: kind, IdempotencyKey: key, Payload: payload})
	return result, err
}

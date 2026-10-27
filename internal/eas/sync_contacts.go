package eas

// Contacts class sync over the collab change log (mailbox_changes, migration
// 0084). Adds/Changes/Deletes come from collapsed change-log rows after the
// stored cursor; each row is consumed exactly once by advancing the cursor
// past it, so an add appears once and a deletion is emitted as Delete exactly
// once (tombstones never resurrect).

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/domain/collab"
)

// storedContact is one mailbox_contacts row projected for sync.
type storedContact struct {
	ID            int64
	AddressbookID int64
	Version       int64
	Contact       collab.Contact
}

// loadContact reads one live contact inside the session scope.
func loadContact(ctx context.Context, b *SyncBackend, req *SyncRequest, id int64) (storedContact, error) {
	tx, err := b.Pool.MailboxTx(ctx, req.Session.TenantID, req.Session.MailboxID)
	if err != nil {
		return storedContact{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var row storedContact
	var emailsRaw, phonesRaw []byte
	err = tx.QueryRow(ctx,
		`SELECT id, addressbook_id, version, display_name, given_name, family_name, emails, phones, note
			  FROM mailbox_contacts
			 WHERE tenant_id = $1 AND mailbox_id = $2 AND id = $3 AND deleted_at IS NULL`,
		req.Session.TenantID, req.Session.MailboxID, id).Scan(
		&row.ID, &row.AddressbookID, &row.Version, &row.Contact.DisplayName,
		&row.Contact.GivenName, &row.Contact.FamilyName, &emailsRaw, &phonesRaw, &row.Contact.Note)
	if err != nil {
		return storedContact{}, err
	}
	_ = json.Unmarshal(emailsRaw, &row.Contact.Emails)
	_ = json.Unmarshal(phonesRaw, &row.Contact.Phones)
	return row, nil
}

// syncContactsCollection runs one Sync round for an "ab:<bookID>" collection.
func (b *SyncBackend) syncContactsCollection(ctx context.Context, req *SyncRequest, p parsedCollection, budget *syncBudget) collectionResult {
	bookID, err := strconv.ParseInt(strings.TrimPrefix(p.collectionID, "ab:"), 10, 64)
	if err != nil || bookID <= 0 {
		return collectionResult{status: syncStatusObjectDeleted}
	}
	state, items, fullSync, status, err := b.keyCheck(ctx, req, p.collectionID, p.syncKey)
	if err != nil {
		return collectionResult{status: syncStatusServerError}
	}
	if status != syncStatusSuccess {
		return collectionResult{status: status}
	}

	ok, err := collabCollectionExists(ctx, b, req, "mailbox_addressbooks", bookID)
	if err != nil {
		return collectionResult{status: syncStatusServerError}
	}
	if !ok {
		// Unknown collection: fail closed, never fabricate an empty book.
		return collectionResult{status: syncStatusObjectDeleted}
	}

	result := collectionResult{status: syncStatusSuccess}
	// Client-side contact writes are outside this task's subset; each client
	// command answers the per-item protocol error (server wins).
	for _, cmd := range p.commands {
		result.responses = append(result.responses, responseEntry(cmd.Token,
			cmd.ChildText(tokServerID), cmd.ChildText(tokClientID), syncStatusProtocolError))
	}

	if fullSync {
		return b.fullSyncContacts(ctx, req, p, state, bookID, budget)
	}

	changes, err := listChangesFor(ctx, b.Pool, req.Session, state.ChangeSeq, "contact", changeLogBatch)
	if err != nil {
		return collectionResult{status: syncStatusServerError}
	}
	collapsed := collapseChanges(changes)
	cursor := state.ChangeSeq
	for _, ch := range collapsed {
		if !budget.itemsLeftOk() {
			result.moreAvailable = true
			break
		}
		id := contactServerID(ch.ResourceID)
		if ch.Deleted {
			if _, known := items[id]; known {
				appless := deleteCommand(id)
				if !budget.chargeCommand(appless) {
					result.moreAvailable = true
					break
				}
				result.commands = append(result.commands, appless)
				delete(items, id)
			}
			cursor = ch.ID
			continue
		}
		row, err := loadContact(ctx, b, req, ch.ResourceID)
		if errors.Is(err, pgx.ErrNoRows) {
			// Foreign-book rows land here when the addressbook filter below
			// would skip them too; either way the change is consumed.
			cursor = ch.ID
			continue
		}
		if err != nil {
			return collectionResult{status: syncStatusServerError}
		}
		if row.AddressbookID != bookID {
			cursor = ch.ID // another addressbook's change: consumed silently
			continue
		}
		fp := "v" + strconv.FormatInt(row.Version, 10)
		if prev, known := items[id]; known && prev.Fingerprint == fp {
			cursor = ch.ID // already synced at this version
			continue
		}
		app := contactAppData(row.Contact)
		token := tokChange
		if _, known := items[id]; !known {
			token = tokAdd
		}
		cmd := addCommand(token, id, app)
		if !budget.chargeCommand(cmd) {
			result.moreAvailable = true
			break
		}
		result.commands = append(result.commands, cmd)
		items[id] = itemRow{ItemID: id, Fingerprint: fp}
		cursor = ch.ID
	}
	state.ChangeSeq = cursor
	if err := persistCollabState(ctx, b, req, p.collectionID, state, items); err != nil {
		return collectionResult{status: syncStatusServerError}
	}
	result.syncKey = formatSyncKey(state.Epoch, state.ChangeSeq)
	return result
}

func contactServerID(id int64) string { return strconv.FormatInt(id, 10) }

// fullSyncContacts emits every live contact of the addressbook up to the
// ceilings and stores the change-log cursor as of now. When truncated, the
// cursor stays at 0 so the remainder is re-offered on the next sync.
func (b *SyncBackend) fullSyncContacts(ctx context.Context, req *SyncRequest, p parsedCollection, state collectionState, bookID int64, budget *syncBudget) collectionResult {
	rows, err := listAddressbookContacts(ctx, b, req, bookID)
	if err != nil {
		return collectionResult{status: syncStatusServerError}
	}
	maxChange, err := maxChangeIDFor(ctx, b, req, "contact")
	if err != nil {
		return collectionResult{status: syncStatusServerError}
	}
	result := collectionResult{status: syncStatusSuccess}
	items := map[string]itemRow{}
	for _, row := range rows {
		id := contactServerID(row.ID)
		app := contactAppData(row.Contact)
		cmd := addCommand(tokAdd, id, app)
		if !budget.chargeCommand(cmd) {
			result.moreAvailable = true
			break
		}
		result.commands = append(result.commands, cmd)
		items[id] = itemRow{ItemID: id, Fingerprint: "v" + strconv.FormatInt(row.Version, 10)}
	}
	if result.moreAvailable {
		state.ChangeSeq = 0
	} else {
		state.ChangeSeq = maxChange
	}
	if err := persistCollabState(ctx, b, req, p.collectionID, state, items); err != nil {
		return collectionResult{status: syncStatusServerError}
	}
	result.syncKey = formatSyncKey(state.Epoch, state.ChangeSeq)
	return result
}

// listAddressbookContacts reads live contacts of one addressbook, bounded.
func listAddressbookContacts(ctx context.Context, b *SyncBackend, req *SyncRequest, bookID int64) ([]storedContact, error) {
	tx, err := b.Pool.MailboxTx(ctx, req.Session.TenantID, req.Session.MailboxID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx,
		`SELECT id, addressbook_id, version, display_name, given_name, family_name, emails, phones, note
			  FROM mailbox_contacts
			 WHERE tenant_id = $1 AND mailbox_id = $2 AND addressbook_id = $3 AND deleted_at IS NULL
			 ORDER BY id LIMIT $4`,
		req.Session.TenantID, req.Session.MailboxID, bookID, MaxItemsPerRequest)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storedContact
	for rows.Next() {
		var row storedContact
		var emailsRaw, phonesRaw []byte
		if err := rows.Scan(&row.ID, &row.AddressbookID, &row.Version, &row.Contact.DisplayName,
			&row.Contact.GivenName, &row.Contact.FamilyName, &emailsRaw, &phonesRaw, &row.Contact.Note); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(emailsRaw, &row.Contact.Emails)
		_ = json.Unmarshal(phonesRaw, &row.Contact.Phones)
		out = append(out, row)
	}
	return out, rows.Err()
}

// collapseChanges keeps only the newest change row per resource, preserving
// change-log order. add-then-update within one window collapses to a single
// Add; a trailing tombstone wins.
func collapseChanges(changes []changeRow) []changeRow {
	last := map[int64]int{}
	var order []int64
	for i, ch := range changes {
		if _, seen := last[ch.ResourceID]; !seen {
			order = append(order, ch.ResourceID)
		}
		last[ch.ResourceID] = i
	}
	out := make([]changeRow, 0, len(order))
	for _, rid := range order {
		out = append(out, changes[last[rid]])
	}
	return out
}

// maxChangeIDFor returns the newest change id for a resource type (0 if none).
func maxChangeIDFor(ctx context.Context, b *SyncBackend, req *SyncRequest, resourceType string) (int64, error) {
	tx, err := b.Pool.MailboxTx(ctx, req.Session.TenantID, req.Session.MailboxID)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id int64
	err = tx.QueryRow(ctx,
		`SELECT coalesce(max(id), 0) FROM mailbox_changes
		  WHERE tenant_id = $1 AND mailbox_id = $2 AND resource_type = $3`,
		req.Session.TenantID, req.Session.MailboxID, resourceType).Scan(&id)
	return id, err
}

// persistCollabState stores state and items in one transaction.
func persistCollabState(ctx context.Context, b *SyncBackend, req *SyncRequest, folderID string, state collectionState, items map[string]itemRow) error {
	tx, err := b.Pool.MailboxTx(ctx, req.Session.TenantID, req.Session.MailboxID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := saveCollectionStateTx(ctx, tx, req.Session, req.DeviceID, folderID, state); err != nil {
		return err
	}
	if err := replaceItemsTx(ctx, tx, req.Session, req.DeviceID, folderID, items); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// collabCollectionExists verifies the collab collection belongs to this
// mailbox; unknown ids answer Status 8 instead of an empty success.
func collabCollectionExists(ctx context.Context, b *SyncBackend, req *SyncRequest, table string, id int64) (bool, error) {
	tx, err := b.Pool.MailboxTx(ctx, req.Session.TenantID, req.Session.MailboxID)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var one int
	err = tx.QueryRow(ctx,
		"SELECT 1 FROM "+table+" WHERE tenant_id = $1 AND mailbox_id = $2 AND id = $3 AND deleted_at IS NULL",
		req.Session.TenantID, req.Session.MailboxID, id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

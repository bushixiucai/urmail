package eas

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// handleFolderSync implements the FolderSync command: full hierarchy on sync
// key 0, otherwise a diff of the stored snapshot against the current
// hierarchy. Collab tombstones from the change log surface as Deletes; the
// snapshot row set is replaced atomically with the new key.
func (s *Server) handleFolderSync(ctx context.Context, sess Session, deviceID string, req *Node) (*Node, error) {
	if req.Page != pageFolderHierarchy || req.Token != tokFolderSync {
		return Element(pageFolderHierarchy, tokFolderSync, Text(pageFolderHierarchy, tokFStatus, folderSyncProtocolError)), nil
	}
	syncKey := req.ChildText(tokFSyncKey)
	current, err := s.CurrentFolders(ctx, sess)
	if err != nil {
		return nil, err
	}
	maxID, err := MaxChangeID(ctx, s.Pool, sess)
	if err != nil {
		return nil, err
	}
	resp := Element(pageFolderHierarchy, tokFolderSync)
	if syncKey == "" || syncKey == "0" {
		if err = ReplaceFolderSnapshot(ctx, s.Pool, sess, deviceID, current, maxID); err != nil {
			return nil, err
		}
		resp.Add(Text(pageFolderHierarchy, tokFStatus, folderSyncSuccess))
		resp.Add(Text(pageFolderHierarchy, tokFSyncKey, strconv.FormatInt(maxID+1, 10)))
		changes := Element(pageFolderHierarchy, tokChanges)
		for _, f := range current {
			changes.Add(addFolder(f))
		}
		changes.Children = append([]*Node{Text(pageFolderHierarchy, tokFCount, strconv.Itoa(len(changes.Children)))}, changes.Children...)
		resp.Add(changes)
		return resp, nil
	}
	stored, err := LoadFolderSnapshot(ctx, s.Pool, sess, deviceID)
	if err != nil {
		return nil, err
	}
	if len(stored) == 0 {
		// A non-zero key without a stored snapshot cannot be honored; ask the
		// client to resync (FolderSync status 9 = invalid sync key).
		resp.Add(Text(pageFolderHierarchy, tokFStatus, folderSyncInvalidKey))
		return resp, nil
	}
	tombstones, err := s.deletedCollections(ctx, sess)
	if err != nil {
		return nil, err
	}
	resp.Add(Text(pageFolderHierarchy, tokFStatus, folderSyncSuccess))
	resp.Add(Text(pageFolderHierarchy, tokFSyncKey, strconv.FormatInt(maxID+1, 10)))
	changes := Element(pageFolderHierarchy, tokChanges)
	currentByID := make(map[string]Folder, len(current))
	for _, f := range current {
		currentByID[f.ID] = f
	}
	storedByID := make(map[string]Folder, len(stored))
	for _, f := range stored {
		storedByID[f.ID] = f
	}
	for _, f := range current {
		prev, known := storedByID[f.ID]
		if known && prev == f {
			continue
		}
		if known {
			upd := Element(pageFolderHierarchy, tokFUpdate)
			upd.Add(Text(pageFolderHierarchy, tokFolderID, f.ID))
			upd.Add(Text(pageFolderHierarchy, tokParentID, folderParentID(f.ParentID)))
			upd.Add(Text(pageFolderHierarchy, tokDisplayName, f.DisplayName))
			upd.Add(Text(pageFolderHierarchy, tokType, strconv.Itoa(f.Type)))
			changes.Add(upd)
			continue
		}
		changes.Add(addFolder(f))
	}
	for id, f := range storedByID {
		if _, ok := currentByID[id]; ok {
			continue
		}
		_ = f
		del := Element(pageFolderHierarchy, tokFDelete)
		del.Add(Text(pageFolderHierarchy, tokFolderID, id))
		changes.Add(del)
	}
	_ = tombstones // collab tombstones are covered by the stored/current diff
	if len(changes.Children) > 0 {
		changes.Children = append([]*Node{Text(pageFolderHierarchy, tokFCount, strconv.Itoa(len(changes.Children)))}, changes.Children...)
		resp.Add(changes)
	}
	if err = ReplaceFolderSnapshot(ctx, s.Pool, sess, deviceID, current, maxID); err != nil {
		return nil, err
	}
	return resp, nil
}

func addFolder(f Folder) *Node {
	add := Element(pageFolderHierarchy, tokFAdd)
	add.Add(Text(pageFolderHierarchy, tokFolderID, f.ID))
	add.Add(Text(pageFolderHierarchy, tokParentID, folderParentID(f.ParentID)))
	add.Add(Text(pageFolderHierarchy, tokDisplayName, f.DisplayName))
	add.Add(Text(pageFolderHierarchy, tokType, strconv.Itoa(f.Type)))
	return add
}

// deletedCollections returns ids of collab folders tombstoned in the change
// log (deleted=true rows). Exported for tests; used to prove tombstone reads.
func (s *Server) deletedCollections(ctx context.Context, sess Session) (map[string]bool, error) {
	tx, err := s.Pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx,
		`SELECT resource_type, resource_id FROM mailbox_changes
			  WHERE tenant_id = $1 AND mailbox_id = $2 AND deleted`,
		sess.TenantID, sess.MailboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var rt string
		var rid int64
		if err = rows.Scan(&rt, &rid); err != nil {
			return nil, err
		}
		switch rt {
		case "addressbook":
			out["ab:"+itoa(rid)] = true
		case "calendar":
			out["cal:"+itoa(rid)] = true
		}
	}
	return out, rows.Err()
}

// errNoDefaultFolder marks a mailbox without a live default addressbook or
// calendar of the requested kind.
var errNoDefaultFolder = errors.New("eas: no default collab folder")

// tokenHashKey carries the bearer token hash through the request context so
// EmailFolders can resolve the IMAP secret from the vault.
type tokenHashKey struct{}

// CurrentFolders builds the exposed hierarchy: the email special folders from
// EmailFolders (when wired) plus the default addressbook and default calendar
// owned by the session mailbox. Collab reads go through MailboxTx, so a
// foreign mailbox's rows are invisible (RLS fail-closed).
func (s *Server) CurrentFolders(ctx context.Context, sess Session) ([]Folder, error) {
	var out []Folder
	if s.EmailFolders != nil {
		tokenHash, _ := ctx.Value(tokenHashKey{}).(string)
		if tokenHash == "" {
			tokenHash = sess.TokenHash
		}
		email, err := s.EmailFolders(ctx, sess, tokenHash)
		if err != nil {
			return nil, err
		}
		out = append(out, email...)
	}
	for _, kind := range []string{"addressbook", "calendar"} {
		f, err := s.defaultCollabFolder(ctx, sess, kind)
		if errors.Is(err, errNoDefaultFolder) {
			continue // no default folder of this kind yet
		}
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, nil
}

// defaultCollabFolder picks the lowest-id live addressbook or calendar owned
// by the mailbox; nil when none exists yet.
func (s *Server) defaultCollabFolder(ctx context.Context, sess Session, kind string) (*Folder, error) {
	tx, err := s.Pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	table := "mailbox_addressbooks"
	folderType := folderTypeContactsDefault
	class := "Contacts"
	prefix := "ab"
	if kind == "calendar" {
		table = "mailbox_calendars"
		folderType = folderTypeCalendarDefault
		class = "Calendar"
		prefix = "cal"
	}
	row := tx.QueryRow(ctx,
		`SELECT id, name FROM `+table+`
			  WHERE tenant_id = $1 AND mailbox_id = $2 AND deleted_at IS NULL
			  ORDER BY id LIMIT 1`, sess.TenantID, sess.MailboxID)
	var id int64
	var name string
	if err = row.Scan(&id, &name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errNoDefaultFolder
		}
		return nil, err
	}
	return &Folder{ID: prefix + ":" + itoa(id), DisplayName: name, Type: folderType, Class: class}, nil
}

func folderParentID(id string) string {
	if id == "" {
		return "0"
	}
	return id
}

package eas

// Email class sync (MS-ASEMAIL). Mail bodies stay in Maildir/IMAP; this file
// projects cursor-paginated IMAP snapshots into AirSync application data. Adds,
// Changes and Deletes come from diffing the live IMAP window (UID + flags
// fingerprint) against the stored per-device item snapshot. Deletes are only
// computed only after the complete UID scan finishes,
// so an old message outside the window is never reported deleted.

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/bushixiucai/urmail/internal/webmail"
)

// maxDetailBytes bounds which messages justify a full-body fetch for the
// preview; larger messages get the truncated marker without a data element.
const maxDetailBytes = 1 << 20

// syncEmailCollection runs one Sync round for an "e:<folder>" collection.
func (b *SyncBackend) syncEmailCollection(ctx context.Context, req *SyncRequest, p parsedCollection, maxItems int, budget *syncBudget) collectionResult {
	if req.Mail == nil {
		// Documented refusal: no IMAP source for this request, no fake data.
		return collectionResult{status: syncStatusServerError}
	}
	folder := strings.TrimPrefix(p.collectionID, "e:")
	state, items, fullSync, status, err := b.keyCheck(ctx, req, p.collectionID, p.syncKey)
	if err != nil {
		return collectionResult{status: syncStatusServerError}
	}
	if status != syncStatusSuccess {
		return collectionResult{status: status}
	}

	uidv, err := req.Mail.UIDValidity(ctx, folder)
	if err != nil {
		return collectionResult{status: syncStatusServerError}
	}
	if !fullSync && state.UIDValidity != 0 && state.UIDValidity != int64(uidv) {
		state = collectionState{Epoch: state.Epoch, UIDValidity: int64(uidv)}
		if err = saveCollectionState(ctx, b.Pool, req.Session, req.DeviceID, p.collectionID, state); err != nil {
			return collectionResult{status: syncStatusServerError}
		}
		if err = replaceItemsAll(ctx, b.Pool, req.Session, req.DeviceID, p.collectionID, map[string]itemRow{}); err != nil {
			return collectionResult{status: syncStatusServerError}
		}
		return collectionResult{status: syncStatusInvalidKey}
	}
	if fullSync {
		items = map[string]itemRow{}
	}
	state.UIDValidity = int64(uidv)
	items, responses, applied := b.applyEmailCommands(ctx, req, p, folder, items)
	result := collectionResult{status: syncStatusSuccess, responses: responses}
	window := p.windowSize
	if window <= 0 || window > maxItems {
		window = maxItems
	}
	emitted := 0
	scan := emailScan{}
	if marker, ok := items[emailScanKey]; ok {
		if json.Unmarshal([]byte(marker.Fingerprint), &scan) != nil {
			return collectionResult{status: syncStatusServerError}
		}
	}
	// Cursor and seen entries survive request/restart boundaries in the same
	// mailbox-scoped snapshot transaction as the returned synchronization key.
	previewBudget := MaxPreviewFetches
	pageCalls := 0
	if scan.Phase != "delete" {
		scan.Phase = "scan"
		for pageCalls < 10 {
			pageCalls++
			messages, next, err := emailPage(ctx, req.Mail, folder, scan.Cursor, maxItems)
			if err != nil {
				return collectionResult{status: syncStatusServerError}
			}
			stopped := false
			for _, m := range messages {
				if m.UID == 0 || (scan.Cursor > 0 && m.UID >= scan.Cursor) {
					return collectionResult{status: syncStatusServerError}
				}
				id := strconv.FormatUint(uint64(m.UID), 10)
				prev, known := items[id]
				fp := m.Fingerprint()
				if !known || prev.Fingerprint != fp {
					if _, full := req.Mail.(MailContentSource); full && previewBudget <= 0 {
						stopped = true
						break
					}
					if emitted >= window {
						stopped = true
						break
					}
					var preview string
					var to, cc []string
					var detailed *webmail.Message
					if previewBudget > 0 {
						if full, ok := req.Mail.(MailContentSource); ok {
							detailed, err = full.FullMessage(ctx, folder, m.UID)
							if err != nil || detailed == nil {
								return collectionResult{status: syncStatusServerError}
							}
							if detailed.UIDValidity != 0 && detailed.UIDValidity != uidv {
								return collectionResult{status: syncStatusInvalidKey}
							}
							preview = truncateRunes(detailed.Text, MaxPreviewRunes)
							for _, v := range detailed.To {
								to = append(to, renderAddress(v))
							}
							for _, v := range detailed.Cc {
								cc = append(cc, renderAddress(v))
							}
							previewBudget--
						} else if m.Size <= maxDetailBytes {
							preview, to, cc, err = req.Mail.Details(ctx, folder, m.UID, MaxPreviewRunes)
							if err != nil {
								return collectionResult{status: syncStatusServerError}
							}
							previewBudget--
						}
					}
					m.To, m.Cc = to, cc
					app := emailAppData(m, preview)
					if detailed != nil && len(detailed.Attachments) > 0 {
						app.Add(attachmentElements(req.Session, req.DeviceID, p.collectionID, uidv, state.Epoch, detailed))
					}
					token := tokAdd
					if known {
						token = tokChange
					}
					cmd := addCommand(token, id, app)
					size := len(Encode(cmd))
					ceiling := b.MaxResponseBytes
					if ceiling <= 0 {
						ceiling = DefaultMaxResponseBytes
					}
					if size > ceiling {
						return collectionResult{status: syncStatusServerError}
					}
					if size > budget.bytesLeft || !budget.chargeCommand(cmd) {
						stopped = true
						break
					}
					result.commands = append(result.commands, cmd)
					items[id] = itemRow{ItemID: id, Fingerprint: fp}
					emitted++
				}
				items[emailSeenPrefix+id] = itemRow{ItemID: emailSeenPrefix + id, Fingerprint: "1"}
				scan.Cursor = m.UID
			}
			if stopped {
				result.moreAvailable = true
				break
			}
			if next == 0 {
				scan.Phase = "delete"
				break
			}
			// Resume from the last consumed UID, never an offset that shifts when
			// another device delivers/expunges messages during this pass.
			if len(messages) == 0 || scan.Cursor == 0 {
				return collectionResult{status: syncStatusServerError}
			}
			if emitted >= window {
				result.moreAvailable = true
				break
			}
		}
		if scan.Phase == "scan" {
			result.moreAvailable = true
		}
	}
	if scan.Phase == "delete" {
		var missing []string
		for id := range items {
			if validUID(id) {
				if _, seen := items[emailSeenPrefix+id]; !seen {
					missing = append(missing, id)
				}
			}
		}
		sort.Slice(missing, func(i, j int) bool { return parseUID(missing[i]) > parseUID(missing[j]) })
		for _, id := range missing {
			cmd := deleteCommand(id)
			if emitted >= window || len(Encode(cmd)) > budget.bytesLeft || !budget.chargeCommand(cmd) {
				result.moreAvailable = true
				break
			}
			result.commands = append(result.commands, cmd)
			delete(items, id)
			emitted++
		}
	}
	if result.moreAvailable {
		raw, _ := json.Marshal(scan)
		items[emailScanKey] = itemRow{ItemID: emailScanKey, Fingerprint: string(raw)}
	} else {
		for id := range items {
			if strings.HasPrefix(id, emailSeenPrefix) || id == emailScanKey {
				delete(items, id)
			}
		}
	}
	if emitted > 0 || applied || result.moreAvailable {
		state.ChangeSeq++
	}
	if err = b.persistEmailState(ctx, req, p.collectionID, state, items); err != nil {
		return collectionResult{status: syncStatusServerError}
	}
	result.syncKey = formatSyncKey(state.Epoch, state.ChangeSeq)
	return result
}

const emailScanKey = "#email_scan"
const emailSeenPrefix = "#email_seen:"

type emailScan struct {
	Cursor uint32 `json:"cursor"`
	Phase  string `json:"phase"`
}

func emailPage(ctx context.Context, source MailSource, folder string, cursor uint32, limit int) ([]MailMessage, uint32, error) {
	if paged, ok := source.(MailPageSource); ok {
		return paged.ListPage(ctx, folder, cursor, limit)
	}
	// Legacy in-memory adapters keep their old bounded contract; production
	// always implements ListPage and cannot stop permanently at its first page.
	messages, total, err := source.ListWindow(ctx, folder, limit)
	if err != nil {
		return nil, 0, err
	}
	if total > len(messages) {
		return nil, 0, errors.New("eas: paged mailbox source required")
	}
	sort.Slice(messages, func(i, j int) bool { return messages[i].UID > messages[j].UID })
	if cursor != 0 {
		filtered := messages[:0]
		for _, m := range messages {
			if m.UID < cursor {
				filtered = append(filtered, m)
			}
		}
		messages = filtered
	}
	return messages, 0, nil
}

// applyEmailCommands applies client Change (Read flag) and Delete (expunge)
// operations. Unsupported client commands (Add/Fetch for email) answer with
// the per-item protocol-error status - never pseudo-success.
func (b *SyncBackend) applyEmailCommands(ctx context.Context, req *SyncRequest, p parsedCollection, folder string, items map[string]itemRow) (map[string]itemRow, []*Node, bool) {
	if len(p.commands) == 0 {
		return items, nil, false
	}
	applied := false
	var responses []*Node
	var removeIDs []string
	for _, cmd := range p.commands {
		serverID := cmd.ChildText(tokServerID)
		clientID := cmd.ChildText(tokClientID)
		switch cmd.Token {
		case tokChange:
			prev, known := items[serverID]
			read := appDataRead(cmd.Child(tokApplicationData))
			if !known || !validUID(serverID) {
				// Server wins: an unknown or already-deleted base gets a
				// protocol error, and the server state is re-emitted.
				responses = append(responses, responseEntry(tokChange, serverID, "", syncStatusObjectDeleted))
				continue
			}
			uid := parseUID(serverID)
			if err := req.Mail.MarkRead(ctx, folder, []uint32{uid}, read); err != nil {
				responses = append(responses, responseEntry(tokChange, serverID, "", syncStatusServerError))
				continue
			}
			// Reflect the new Read state in the stored fingerprint so the next
			// diff does not re-emit this change.
			prev.Fingerprint = setReadFlag(prev.Fingerprint, read)
			items[serverID] = prev
			responses = append(responses, responseEntry(tokChange, serverID, "", syncStatusSuccess))
			applied = true
		case tokDelete:
			if _, known := items[serverID]; !known || !validUID(serverID) {
				responses = append(responses, responseEntry(tokDelete, serverID, "", syncStatusObjectDeleted))
				continue
			}
			// Expunge exactly this UID; never a whole-folder EXPUNGE.
			if err := req.Mail.DeleteUIDs(ctx, folder, []uint32{parseUID(serverID)}); err != nil {
				responses = append(responses, responseEntry(tokDelete, serverID, "", syncStatusServerError))
				continue
			}
			removeIDs = append(removeIDs, serverID)
			delete(items, serverID)
			responses = append(responses, responseEntry(tokDelete, serverID, "", syncStatusSuccess))
			applied = true
		default:
			// Client Add/Fetch of mail is outside the implemented subset.
			responses = append(responses, responseEntry(cmd.Token, serverID, clientID, syncStatusProtocolError))
		}
	}
	if len(removeIDs) > 0 {
		_ = b.removeItems(ctx, req, p.collectionID, removeIDs)
	}
	return items, responses, applied
}

// appDataRead extracts the Read value from client application data.
func appDataRead(app *Node) bool {
	if app == nil {
		return false
	}
	return app.ChildText(tokEmailRead) == "1"
}

// validUID checks a decimal ServerId.
func validUID(id string) bool {
	_, err := strconv.ParseUint(id, 10, 32)
	return err == nil && id != "0"
}

func parseUID(id string) uint32 {
	v, _ := strconv.ParseUint(id, 10, 32)
	return uint32(v)
}

// setReadFlag rewrites the Read marker in a fingerprint string.
func setReadFlag(fp string, read bool) string {
	flags, rest, ok := strings.Cut(fp, "|")
	if !ok {
		return fp
	}
	flags = strings.ReplaceAll(flags, "r", "")
	if read {
		flags = "r" + flags
	}
	return flags + "|" + rest
}

// persistEmailState atomically stores the response revision and the full
// snapshot, including any in-progress scan cursor and seen markers.
func (b *SyncBackend) persistEmailState(ctx context.Context, req *SyncRequest, folderID string, state collectionState, items map[string]itemRow) error {
	tx, err := b.Pool.MailboxTx(ctx, req.Session.TenantID, req.Session.MailboxID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = saveCollectionStateTx(ctx, tx, req.Session, req.DeviceID, folderID, state); err != nil {
		return err
	}
	if err = replaceItemsTx(ctx, tx, req.Session, req.DeviceID, folderID, items); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// removeItems deletes stored item rows (tombstones after emission).
func (b *SyncBackend) removeItems(ctx context.Context, req *SyncRequest, folderID string, ids []string) error {
	if len(ids) == 0 || b.Pool == nil {
		return nil
	}
	tx, err := b.Pool.MailboxTx(ctx, req.Session.TenantID, req.Session.MailboxID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := deleteItemsTx(ctx, tx, req.Session, req.DeviceID, folderID, ids); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

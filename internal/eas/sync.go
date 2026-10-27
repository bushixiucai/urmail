package eas

// Sync command implementation (Task EAS-003). The backend owns the request
// parse, per-collection sync key state, windowing and ceilings. Conflict
// policy: the server always wins. A client Change is applied only when the
// item is still in the device's synced set (i.e. the client had the base
// state); anything else - an unknown ServerId, an item deleted elsewhere, a
// stale base - answers with the documented item status and the server state
// is re-emitted on the next sync. Tombstones never resurrect: an item removed
// from the stored set only reappears through a new change-log entry, a live
// IMAP UID, or a fresh full sync (key 0).

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

// Server-side ceilings. The client window is clamped to MaxItems and every
// response is additionally bounded in items per request and approximate WBXML
// bytes per request.
const (
	// DefaultMaxItems is the per-collection item cap.
	DefaultMaxItems = 100
	// MaxItemsPerRequest caps emitted commands across all collections.
	MaxItemsPerRequest = 300
	// DefaultMaxResponseBytes is the approximate response byte ceiling.
	DefaultMaxResponseBytes = 512 << 10
	// MaxPreviewRunes bounds the body preview (truncation, not a second body
	// store: mail bodies stay in Maildir/IMAP).
	MaxPreviewRunes = 255
	// MaxPreviewFetches bounds the per-request full-message fetches used for
	// previews and To/Cc on email Adds.
	MaxPreviewFetches = 10
	// changeLogBatch bounds one change-log scan.
	changeLogBatch = 500
)

// Dedicated guard connections are process-bounded as well as request-timed.
var syncGuardSlots = make(chan struct{}, 8)

// SyncRequest carries everything HandleSync needs beyond the parsed node.
type SyncRequest struct {
	Session  Session
	DeviceID string
	Node     *Node
	// Mail is the per-request email source; nil means email collections
	// answer with the documented server-error status.
	Mail MailSource
}

// SyncBackend is the EAS-003 Sync handler.
type SyncBackend struct {
	Pool *db.Pool
	// Mail builds the per-request IMAP source.
	Mail MailSourceFactory
	// MaxItems clamps the client window per collection. Zero uses DefaultMaxItems.
	MaxItems int
	// MaxResponseBytes bounds the emitted response. Zero uses the default.
	MaxResponseBytes int
}

// wantsMail tells the transport this backend consumes the per-request email
// source. Kept unexported: it is transport wiring, not API.
func (b *SyncBackend) wantsMail() bool { return true }

// NewSyncBackend returns a backend with defaults applied.
func NewSyncBackend(pool *db.Pool) *SyncBackend {
	return &SyncBackend{Pool: pool, MaxItems: DefaultMaxItems, MaxResponseBytes: DefaultMaxResponseBytes}
}

// collectionResult is one collection's outcome.
type collectionResult struct {
	syncKey       string
	status        string
	commands      []*Node
	responses     []*Node
	moreAvailable bool
}

// HandleSync implements SyncHandler.
func (b *SyncBackend) HandleSync(ctx context.Context, req *SyncRequest) (*Node, error) {
	resp := Element(pageAirSync, tokSync)
	if req == nil || req.Node == nil || req.Node.Token != tokSync || req.Node.Page != pageAirSync {
		resp.Add(Text(pageAirSync, tokStatus, syncStatusProtocolError))
		return resp, nil
	}
	cols := req.Node.Child(tokCollections)
	if cols == nil || len(cols.Children) == 0 {
		resp.Add(Text(pageAirSync, tokStatus, syncStatusProtocolError))
		return resp, nil
	}
	if b.Pool == nil {
		return resp.Add(Text(pageAirSync, tokStatus, syncStatusServerError)), nil
	}
	// A guard must not occupy the business pool: all collection helpers use
	// their own scoped transactions, including installations with MaxConns=1.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case syncGuardSlots <- struct{}{}:
		defer func() { <-syncGuardSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	guardConn, err := pgx.ConnectConfig(ctx, b.Pool.Config().ConnConfig.Copy())
	if err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = guardConn.Close(cleanup)
	}()
	guard, err := guardConn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = guard.Rollback(cleanup)
	}()
	if err = store.New(guard).LockMailboxEASSync(ctx, store.LockMailboxEASSyncParams{TenantID: strconv.FormatInt(req.Session.TenantID, 10), MailboxID: strconv.FormatInt(req.Session.MailboxID, 10), DeviceID: req.DeviceID}); err != nil {
		return nil, err
	}

	maxItems := b.MaxItems
	if maxItems <= 0 {
		maxItems = DefaultMaxItems
	}
	maxBytes := b.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxResponseBytes
	}
	budget := &syncBudget{itemsLeft: MaxItemsPerRequest, bytesLeft: maxBytes}
	collectionsNode := Element(pageAirSync, tokCollections)
	for _, col := range cols.Children {
		if col.Token != tokCollection {
			resp.Children = nil
			resp.Add(Text(pageAirSync, tokStatus, syncStatusProtocolError))
			return resp, nil
		}
		if budget.itemsLeft <= 0 || budget.bytesLeft <= 0 {
			break // request ceilings hit: remaining collections wait for the next sync
		}
		parsed := parseCollection(col)
		result := b.handleCollection(ctx, req, parsed, maxItems, budget)
		collectionsNode.Add(renderCollection(parsed.collectionID, result))
	}
	if len(collectionsNode.Children) == 0 {
		// Every collection was starved by the request ceilings.
		resp.Add(Text(pageAirSync, tokStatus, syncStatusProtocolError))
		return resp, nil
	}
	resp.Add(collectionsNode)
	return resp, nil
}

// syncBudget tracks the per-request item and byte ceilings.
type syncBudget struct {
	itemsLeft int
	bytesLeft int
}

// itemsLeftOk reports whether another command may be emitted.
func (b *syncBudget) itemsLeftOk() bool { return b.itemsLeft > 0 && b.bytesLeft > 0 }

// chargeCommand accounts one emitted command element; returns false when the
// budget is spent and the caller must stop and report MoreAvailable.
func (b *syncBudget) chargeCommand(cmd *Node) bool {
	if b.itemsLeft <= 0 || b.bytesLeft <= 0 {
		return false
	}
	size := len(Encode(cmd))
	if size > b.bytesLeft {
		return false
	}
	b.itemsLeft--
	b.bytesLeft -= size
	return true
}

// parsedCollection is the decoded client collection element.
type parsedCollection struct {
	syncKey      string
	collectionID string
	windowSize   int
	// commands are the client-side operations in request order.
	commands []*Node
	// raw points at the request element for diagnostics.
	raw *Node
}

func parseCollection(col *Node) parsedCollection {
	p := parsedCollection{syncKey: "0", collectionID: col.ChildText(tokCollectionID), raw: col}
	if k := col.ChildText(tokSyncKey); k != "" {
		p.syncKey = k
	}
	if ws := col.ChildText(tokWindowSize); ws != "" {
		if n, err := strconv.Atoi(ws); err == nil && n > 0 {
			p.windowSize = n
		}
	}
	commandNodes := col.Children
	if commands := nodeOnPage(col, pageAirSync, tokCommands); commands != nil {
		commandNodes = commands.Children
	}
	for _, c := range commandNodes {
		switch c.Token {
		case tokAdd, tokChange, tokDelete, tokFetch:
			p.commands = append(p.commands, c)
		}
	}
	return p
}

// handleCollection dispatches one collection to its class handler. Unknown
// collection ids answer Status 8 (fail closed: a foreign collection never
// resolves inside this mailbox's RLS scope).
func (b *SyncBackend) handleCollection(ctx context.Context, req *SyncRequest, p parsedCollection, maxItems int, budget *syncBudget) collectionResult {
	switch {
	case strings.HasPrefix(p.collectionID, "e:"):
		return b.syncEmailCollection(ctx, req, p, maxItems, budget)
	case strings.HasPrefix(p.collectionID, "ab:"):
		return b.syncContactsCollection(ctx, req, p, budget)
	case strings.HasPrefix(p.collectionID, "cal:"):
		return b.syncCalendarCollection(ctx, req, p, budget)
	default:
		return collectionResult{status: syncStatusObjectDeleted}
	}
}

// renderCollection serializes one result element. Element order follows the
// MS-ASCMD Sync collection schema: SyncKey, CollectionId, Status, Responses,
// Commands, MoreAvailable.
func renderCollection(collectionID string, r collectionResult) *Node {
	col := Element(pageAirSync, tokCollection)
	if r.status == syncStatusSuccess {
		col.Add(Text(pageAirSync, tokSyncKey, r.syncKey))
	}
	col.Add(Text(pageAirSync, tokCollectionID, collectionID))
	col.Add(Text(pageAirSync, tokStatus, r.status))
	if len(r.responses) > 0 {
		responses := Element(pageAirSync, tokResponses)
		responses.Children = r.responses
		col.Add(responses)
	}
	if len(r.commands) > 0 {
		commands := Element(pageAirSync, tokCommands)
		commands.Children = r.commands
		col.Add(commands)
	}
	if r.moreAvailable {
		col.Add(Element(pageAirSync, tokMoreAvailable))
	}
	return col
}

// addCommand builds a server-side Add/Change/Delete command element.
func addCommand(token byte, serverID string, appData *Node) *Node {
	cmd := Element(pageAirSync, token)
	cmd.Add(Text(pageAirSync, tokServerID, serverID))
	cmd.Add(appData)
	return cmd
}

// deleteCommand builds a server-side Delete command element.
func deleteCommand(serverID string) *Node {
	return Element(pageAirSync, tokDelete).Add(Text(pageAirSync, tokServerID, serverID))
}

// responseEntry builds a per-client-command response element.
func responseEntry(token byte, serverID, clientID, status string) *Node {
	entry := Element(pageAirSync, token)
	if serverID != "" {
		entry.Add(Text(pageAirSync, tokServerID, serverID))
	}
	if clientID != "" {
		entry.Add(Text(pageAirSync, tokClientID, clientID))
	}
	entry.Add(Text(pageAirSync, tokStatus, status))
	return entry
}

// tokCommands is page 0 0x16 (Commands element).
const tokCommands = byte(0x16)

// keyCheck validates the client key against stored state. It returns the
// working state, whether this is a full sync, and the status to answer with
// (success when valid, Status 9 after invalidating the stored state).
func (b *SyncBackend) keyCheck(ctx context.Context, req *SyncRequest, folderID, key string) (collectionState, map[string]itemRow, bool, string, error) {
	if b.Pool == nil {
		return collectionState{}, nil, false, syncStatusServerError, nil
	}
	var state collectionState
	stateExists := false
	var items map[string]itemRow
	st, err := loadCollectionState(ctx, b.Pool, req.Session, req.DeviceID, folderID)
	switch {
	case err == nil:
		stateExists = true
		state = st
	case errors.Is(err, ErrNotFound):
		epoch, epochErr := store.New(b.Pool.Pool).PlatformRestoreSyncEpoch(ctx)
		if epochErr != nil {
			return state, nil, false, syncStatusServerError, epochErr
		}
		state = collectionState{Epoch: epoch}
	default:
		return state, nil, false, syncStatusServerError, err
	}
	if items, err = loadItems(ctx, b.Pool, req.Session, req.DeviceID, folderID); err != nil {
		return state, nil, false, syncStatusServerError, err
	}
	epoch, seq, fullSync, ok := parseSyncKey(key)
	if !ok || (!fullSync && (!stateExists || epoch != state.Epoch || seq != state.ChangeSeq)) {
		// Reject stale or malformed keys without destroying the valid snapshot.
		// A subsequent explicit key 0 starts a new generation.
		return state, items, false, syncStatusInvalidKey, nil
	}
	if fullSync {
		var generation [8]byte
		if _, err = rand.Read(generation[:]); err != nil {
			return state, nil, false, syncStatusServerError, err
		}
		epoch := int64(binary.BigEndian.Uint64(generation[:]) & ((1 << 63) - 1))
		if epoch == 0 {
			epoch = 1
		}
		state = collectionState{Epoch: epoch}
		items = map[string]itemRow{}
	}
	return state, items, fullSync, syncStatusSuccess, nil
}

// replaceItemsAll rewrites the whole item set for a collection.
func replaceItemsAll(ctx context.Context, pool *db.Pool, sess Session, deviceID, folderID string, items map[string]itemRow) error {
	tx, err := pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := replaceItemsTx(ctx, tx, sess, deviceID, folderID, items); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

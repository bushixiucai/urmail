package eas

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/store"
)

const maxHeartbeatSeconds = 3600
const maxPingFolders = 100

type pingFolder struct {
	ID     string
	Class  string
	Cursor uint32 `json:"cursor,omitempty"`
}

func pingStatus(status string) *Node {
	return Element(pagePing, tokPing, Text(pagePing, tokPStatus, status))
}

// handlePing compares each monitored collection with the device's last Sync,
// so changes committed before the HTTP request are never lost at a baseline
// boundary. Configuration survives both server restarts and empty Ping bodies.
func (s *Server) handlePing(ctx context.Context, sess Session, deviceID string, req *Node) (*Node, error) {
	if req.Page != pagePing || req.Token != tokPing {
		return pingStatus(pingProtocolError), nil
	}
	tx, err := s.Pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	saved, err := q.GetMailboxEASPingConfig(ctx, store.GetMailboxEASPingConfigParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, DeviceID: deviceID})
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	budget := s.PingBudget
	if budget <= 0 {
		budget = DefaultPingBudget
	}
	maximum := int64(budget / time.Second)
	if maximum < 1 {
		maximum = 1
	}
	if maximum > maxHeartbeatSeconds {
		maximum = maxHeartbeatSeconds
	}
	hb := int64(saved.PingHeartbeat)
	if text := req.ChildText(tokHeartbeat); text != "" {
		hb, err = strconv.ParseInt(text, 10, 32)
		if err != nil {
			return pingStatus(pingProtocolError), nil
		}
	}
	if hb < 1 || hb > maximum {
		if hb == 0 && req.ChildText(tokHeartbeat) == "" {
			return pingStatus("3"), nil
		}
		out := pingStatus(pingHbOutOfRange)
		limit := maximum
		if hb < 1 {
			limit = 1
		}
		out.Add(Text(pagePing, tokHeartbeat, strconv.FormatInt(limit, 10)))
		return out, nil
	}
	var folders []pingFolder
	if err = json.Unmarshal(saved.PingFolders, &folders); err != nil {
		return nil, err
	}
	if node := req.Child(tokPFolders); node != nil {
		folders = nil
		for _, f := range node.Children {
			if f.Page != pagePing || f.Token != tokPFolder || f.ChildText(tokPId) == "" || f.ChildText(tokPClass) == "" {
				return pingStatus(pingProtocolError), nil
			}
			folders = append(folders, pingFolder{ID: f.ChildText(tokPId), Class: f.ChildText(tokPClass)})
		}
	}
	if len(folders) == 0 {
		return pingStatus("3"), nil
	}
	if len(folders) > maxPingFolders {
		out := pingStatus("6")
		out.Add(Text(pagePing, tokPFolderType, strconv.Itoa(maxPingFolders)))
		return out, nil
	}
	current, err := s.CurrentFolders(ctx, sess)
	if err != nil {
		return nil, err
	}
	allowed := map[string]string{}
	for _, f := range current {
		allowed[f.ID] = f.Class
	}
	seen := map[string]bool{}
	for _, f := range folders {
		if allowed[f.ID] != f.Class || seen[f.ID] {
			return pingStatus("7"), nil
		}
		seen[f.ID] = true
	}
	raw, err := json.Marshal(folders)
	if err != nil {
		return nil, err
	}
	tx, err = s.Pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if err != nil {
		return nil, err
	}
	count, err := store.New(tx).SetMailboxEASPingConfig(ctx, store.SetMailboxEASPingConfigParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, DeviceID: deviceID, PingHeartbeat: int32(hb), PingFolders: raw})
	if err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, errors.New("eas: device disappeared during Ping")
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	wait := time.Duration(hb) * time.Second
	if wait > budget {
		wait = budget
	}
	pollCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	interval := s.PollInterval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	var mail MailSource
	for _, f := range folders {
		if f.Class == "Email" && mail == nil {
			if s.Mail == nil {
				return pingStatus(pingServerError), nil
			}
			mail, err = s.Mail(pollCtx, sess)
			if err != nil || mail == nil {
				return pingStatus(pingServerError), nil
			}
			defer func() { _ = mail.Close() }()
		}
	}
	for {
		changed := []string{}
		for i := range folders {
			f := &folders[i]
			yes, checkErr := s.pingCollectionChanged(pollCtx, sess, deviceID, f, mail)
			if checkErr != nil {
				if pollCtx.Err() != nil && ctx.Err() == nil {
					return pingStatus(pingNoChanges), nil
				}
				return nil, checkErr
			}
			if yes {
				changed = append(changed, f.ID)
			}
		}
		if len(changed) > 0 {
			sort.Strings(changed)
			out := pingStatus(pingChanges)
			fs := Element(pagePing, tokPFolders)
			// MS-ASCMD Folder is a string in responses, not a request-shaped Id child.
			for _, id := range changed {
				fs.Add(Text(pagePing, tokPFolder, id))
			}
			out.Add(fs)
			return out, nil
		}
		// The next bounded scan resumes after a restart as well as the next
		// poll, so deep flag-only changes do not starve behind the head page.
		encoded, err := json.Marshal(folders)
		if err != nil {
			return nil, err
		}
		progress, err := s.Pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
		if err != nil {
			return nil, err
		}
		_, writeErr := store.New(progress).SetMailboxEASPingConfig(ctx, store.SetMailboxEASPingConfigParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, DeviceID: deviceID, PingHeartbeat: int32(hb), PingFolders: encoded})
		if writeErr == nil {
			writeErr = progress.Commit(ctx)
		} else {
			_ = progress.Rollback(ctx)
		}
		if writeErr != nil {
			return nil, writeErr
		}
		timer := time.NewTimer(interval)
		select {
		case <-pollCtx.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return pingStatus(pingNoChanges), nil
		case <-timer.C:
		}
	}
}

func (s *Server) pingCollectionChanged(ctx context.Context, sess Session, deviceID string, f *pingFolder, mail MailSource) (bool, error) {
	state, err := loadCollectionState(ctx, s.Pool, sess, deviceID, f.ID)
	if errors.Is(err, ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if f.Class == "Email" {
		uidv, err := mail.UIDValidity(ctx, strings.TrimPrefix(f.ID, "e:"))
		if err != nil {
			return false, err
		}
		if int64(uidv) != state.UIDValidity {
			return true, nil
		}
		items, err := loadItems(ctx, s.Pool, sess, deviceID, f.ID)
		if err != nil {
			return false, err
		}
		messages, total, err := mail.ListWindow(ctx, strings.TrimPrefix(f.ID, "e:"), DefaultMaxItems)
		if err != nil {
			return false, err
		}
		count := 0
		for id := range items {
			if !strings.Contains(id, "#") {
				count++
			}
		}
		if total != count {
			return true, nil
		}
		for _, m := range messages {
			if previous, ok := items[strconv.FormatUint(uint64(m.UID), 10)]; !ok || previous.Fingerprint != m.Fingerprint() {
				return true, nil
			}
		}
		if pages, ok := mail.(MailPageSource); ok && total > len(messages) {
			page, next, err := pages.ListPage(ctx, strings.TrimPrefix(f.ID, "e:"), f.Cursor, DefaultMaxItems)
			if err != nil {
				return false, err
			}
			f.Cursor = next
			for _, m := range page {
				if previous, ok := items[strconv.FormatUint(uint64(m.UID), 10)]; !ok || previous.Fingerprint != m.Fingerprint() {
					return true, nil
				}
			}
		}
		return false, nil
	}
	kind, id, ok := strings.Cut(f.ID, ":")
	if !ok {
		return false, errors.New("eas: invalid monitored collection")
	}
	resourceID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return false, err
	}
	switch kind {
	case "ab":
		kind = "addressbook"
	case "cal":
		kind = "calendar"
	default:
		return false, errors.New("eas: invalid monitored class")
	}
	tx, err := s.Pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return store.New(tx).MailboxEASPingCollectionChanged(ctx, store.MailboxEASPingCollectionChangedParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, ResourceType: kind, ResourceID: resourceID, ID: state.ChangeSeq})
}

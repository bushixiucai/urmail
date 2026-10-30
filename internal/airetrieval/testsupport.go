package airetrieval

import (
	"context"
	"fmt"
	"strings"
)

// FakeStore is an in-memory ChunkStore exported for sibling handler tests.
// It honors the principal on every access exactly like the PG implementation.
type FakeStore struct {
	chunks map[int64]Chunk // global chunk registry; access still scoped
	owner  map[int64]Principal
	byMsg  map[Principal]map[MessageKey][]int64
	nextID int64
}

// NewFakeStore returns an empty in-memory ChunkStore.
func NewFakeStore() *FakeStore {
	return &FakeStore{chunks: map[int64]Chunk{}, owner: map[int64]Principal{}, byMsg: map[Principal]map[MessageKey][]int64{}}
}

// MessageStates returns the indexed digest and version per message.
func (f *FakeStore) MessageStates(_ context.Context, p Principal) (map[MessageKey]MessageState, error) {
	out := map[MessageKey]MessageState{}
	for key, ids := range f.byMsg[p] {
		if len(ids) == 0 {
			continue
		}
		chunk := f.chunks[ids[0]]
		out[key] = MessageState{Digest: chunk.Digest, Version: chunk.ContentVersion}
	}
	return out, nil
}

// ReplaceMessage atomically rewrites one message's chunks.
func (f *FakeStore) ReplaceMessage(_ context.Context, p Principal, folder string, uidValidity, uid uint32, version int, digest string, contents []string) error {
	if f.byMsg[p] == nil {
		f.byMsg[p] = map[MessageKey][]int64{}
	}
	f.deleteLocked(p, MessageKey{Folder: folder, UIDValidity: uidValidity, UID: uid})
	for i, content := range contents {
		f.nextID++
		f.chunks[f.nextID] = Chunk{ID: f.nextID, Folder: folder, UIDValidity: uidValidity, UID: uid, ChunkIndex: i, ContentVersion: version, Content: content, Digest: digest}
		f.owner[f.nextID] = p
		f.byMsg[p][MessageKey{Folder: folder, UIDValidity: uidValidity, UID: uid}] = append(f.byMsg[p][MessageKey{Folder: folder, UIDValidity: uidValidity, UID: uid}], f.nextID)
	}
	return nil
}

func (f *FakeStore) deleteLocked(p Principal, key MessageKey) {
	for _, id := range f.byMsg[p][key] {
		delete(f.chunks, id)
		delete(f.owner, id)
	}
	delete(f.byMsg[p], key)
}

// DeleteMessages removes the chunks of expunged messages.
func (f *FakeStore) DeleteMessages(_ context.Context, p Principal, folder string, uidValidity uint32, uids []uint32) error {
	for _, uid := range uids {
		f.deleteLocked(p, MessageKey{Folder: folder, UIDValidity: uidValidity, UID: uid})
	}
	return nil
}

// DeleteFolder drops every chunk of one folder.
func (f *FakeStore) DeleteFolder(_ context.Context, p Principal, folder string) error {
	for key := range f.byMsg[p] {
		if key.Folder == folder {
			f.deleteLocked(p, key)
		}
	}
	return nil
}

// PurgeChunks purges every chunk belonging to this principal.
func (f *FakeStore) PurgeChunks(_ context.Context, p Principal) error {
	for id, owner := range f.owner {
		if owner == p {
			delete(f.chunks, id)
			delete(f.owner, id)
		}
	}
	delete(f.byMsg, p)
	return nil
}

// SearchChunks returns principal-scoped chunks whose content matches query.
func (f *FakeStore) SearchChunks(_ context.Context, p Principal, query string, limit int) ([]Chunk, error) {
	var out []Chunk
	for id, chunk := range f.chunks {
		if f.owner[id] != p {
			continue // principal-scoped, like the SQL predicates
		}
		if contains(chunk.Content, query) {
			out = append(out, chunk)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func contains(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

// GetChunk re-reads one chunk under the principal.
func (f *FakeStore) GetChunk(_ context.Context, p Principal, id int64) (Chunk, bool, error) {
	chunk, ok := f.chunks[id]
	if !ok || f.owner[id] != p {
		return Chunk{}, false, nil // another mailbox's chunk is not found
	}
	return chunk, true, nil
}

// FakeSource is a MailSource with mutable live state.
type FakeSource struct {
	Mail       map[string]map[uint32]string // folder -> uid -> body
	HTML       map[string]map[uint32]string
	Subject    map[string]map[uint32]string
	From       map[string]map[uint32]string
	Validity   map[string]uint32
	FetchCount int
}

// Folders lists the folders with live mail.
func (s *FakeSource) Folders(context.Context) ([]string, error) {
	all := map[string]bool{}
	for name := range s.Mail {
		all[name] = true
	}
	for name := range s.HTML {
		all[name] = true
	}
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	return names, nil
}

// MessageUIDs returns the folder's UIDVALIDITY and live uids.
func (s *FakeSource) MessageUIDs(_ context.Context, folder string) (uint32, []uint32, error) {
	uidsMap := map[uint32]bool{}
	for uid := range s.Mail[folder] {
		uidsMap[uid] = true
	}
	for uid := range s.HTML[folder] {
		uidsMap[uid] = true
	}
	uids := make([]uint32, 0, len(uidsMap))
	for uid := range uidsMap {
		uids = append(uids, uid)
	}
	return s.Validity[folder], uids, nil
}

// MessageText returns one message body.
func (s *FakeSource) MessageText(_ context.Context, folder string, uid uint32) (string, error) {
	s.FetchCount++
	var body string
	if text, ok := s.Mail[folder][uid]; ok {
		body = strings.TrimSpace(text)
	}
	if body == "" && s.HTML != nil {
		if h, ok := s.HTML[folder][uid]; ok && strings.TrimSpace(h) != "" {
			body = ExtractHTMLText(h)
		}
	}
	var headers []string
	if s.Subject != nil {
		if subj, ok := s.Subject[folder][uid]; ok && strings.TrimSpace(subj) != "" {
			headers = append(headers, "Subject: "+strings.TrimSpace(subj))
		}
	}
	if s.From != nil {
		if from, ok := s.From[folder][uid]; ok && strings.TrimSpace(from) != "" {
			headers = append(headers, "From: "+strings.TrimSpace(from))
		}
	}
	header := strings.Join(headers, "\n")
	if header != "" && body != "" {
		return header + "\n\n" + body, nil
	}
	if header != "" {
		return header, nil
	}
	if body == "" {
		return "", fmt.Errorf("message gone: %s/%d", folder, uid)
	}
	return body, nil
}

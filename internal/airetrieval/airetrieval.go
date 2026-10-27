// Package airetrieval builds a permission-aware per-mailbox retrieval index
// over searchable mail content and serves cross-mail Q&A answers whose
// citations can only reference chunks the current mailbox principal still
// reads. PostgreSQL is the only authority for indexed content; the mail
// source abstraction is only a reader used to refresh the index and to
// re-verify message liveness before anything is returned.
package airetrieval

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Principal identifies the mailbox-scoped caller. Tenant admins and platform
// roles never gain implicit access: every operation is keyed by this pair.
type Principal struct {
	TenantID  int64
	MailboxID int64
}

// Valid reports whether the principal is usable.
func (p Principal) Valid() bool {
	return p.TenantID > 0 && p.MailboxID > 0
}

// Chunk is one indexed piece of one message of one mailbox.
type Chunk struct {
	ID             int64
	Folder         string
	UIDValidity    uint32
	UID            uint32
	ChunkIndex     int
	ContentVersion int
	Content        string
	Digest         string // SHA-256 of the full indexed message text
}

// MessageState summarizes what is currently indexed for one message.
type MessageState struct {
	Digest  string
	Version int
}

const maxChunkRunes = 2000

// ChunkText splits message text into bounded chunks without splitting runes.
// Empty text produces no chunks: an unindexable message is simply absent.
func ChunkText(text string) []string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	var chunks []string
	for len(trimmed) > 0 {
		if utf8.RuneCountInString(trimmed) <= maxChunkRunes {
			chunks = append(chunks, strings.TrimSpace(trimmed))
			break
		}
		cut := 0
		for i := 0; i < maxChunkRunes; i++ {
			_, size := utf8.DecodeRuneInString(trimmed[cut:])
			cut += size
		}
		chunks = append(chunks, strings.TrimSpace(trimmed[:cut]))
		trimmed = strings.TrimSpace(trimmed[cut:])
	}
	return chunks
}

// ContentDigest is the stable indexed-content fingerprint of one message.
// Reindexing is incremental: a message whose digest is unchanged is skipped.
func ContentDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// CitationID is the citation identifier of a chunk: the numeric chunk row id.
// It contains no whitespace, satisfying the aianswer.Source id rules.
func (c Chunk) CitationID() string { return strconv.FormatInt(c.ID, 10) }

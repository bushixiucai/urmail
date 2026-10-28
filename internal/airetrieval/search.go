package airetrieval

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Errors reported by the retrieval pipeline.
var (
	ErrNoPrincipal = errors.New("airetrieval: missing mailbox principal")
	ErrNoSource    = errors.New("airetrieval: missing mail source or store")
	ErrInvalidAsk  = errors.New("airetrieval: invalid ask request")
)

const maxSearchHits = 20

// Search returns permission-filtered chunks for the principal. Every access
// point re-verifies authorization:
//
//  1. the store query is tenant/mailbox scoped (and RLS fail-closes without
//     the mailbox context);
//  2. each hit is re-read by id under the same principal (a stale cache or a
//     chunk that moved to another mailbox yields not-found and is dropped);
//  3. the source message must still be live (listed under the folder's
//     current UIDVALIDITY), so deleted or expunged mail never comes back.
//
// A revoked or deleted mailbox produces zero results because neither the
// store rows nor the live listing survive.
func Search(ctx context.Context, store ChunkStore, source MailSource, p Principal, query string) ([]Chunk, error) {
	if !p.Valid() {
		return nil, ErrNoPrincipal
	}
	if store == nil || source == nil {
		return nil, ErrNoSource
	}
	needle := strings.TrimSpace(query)
	if needle == "" {
		return nil, nil
	}
	// Retrieval uses bounded lexical terms instead of requiring the complete
	// natural-language question to appear verbatim inside a message.
	needles := queryNeedles(needle)
	candidates := []Chunk{}
	seen := map[int64]bool{}
	for _, term := range needles {
		hits, err := store.SearchChunks(ctx, p, term, maxSearchHits)
		if err != nil {
			return nil, err
		}
		for _, chunk := range hits {
			if !seen[chunk.ID] {
				seen[chunk.ID] = true
				candidates = append(candidates, chunk)
			}
		}
	}
	score := func(chunk Chunk) int {
		content := strings.ToLower(chunk.Content)
		n := 0
		for _, term := range needles {
			if strings.Contains(content, strings.ToLower(term)) {
				n++
			}
		}
		return n
	}
	sort.SliceStable(candidates, func(i, j int) bool { return score(candidates[i]) > score(candidates[j]) })
	if len(candidates) > maxSearchHits {
		candidates = candidates[:maxSearchHits]
	}
	liveFolders := map[string]map[uint32]bool{}
	var verified []Chunk
	for _, candidate := range candidates {
		// (2) re-read under the principal: catches cross-mailbox drift.
		chunk, found, err := store.GetChunk(ctx, p, candidate.ID)
		if err != nil {
			return nil, err
		}
		if !found || chunk.Folder != candidate.Folder || chunk.UID != candidate.UID || chunk.UIDValidity != candidate.UIDValidity {
			continue
		}
		// (3) message still readable in the live mailbox.
		live, err := liveUIDs(ctx, source, liveFolders, chunk.Folder, chunk.UIDValidity)
		if err != nil {
			return nil, err
		}
		if !live[chunk.UID] {
			continue
		}
		verified = append(verified, chunk)
	}
	return verified, nil
}

func liveUIDs(ctx context.Context, source MailSource, cache map[string]map[uint32]bool, folder string, expectedValidity uint32) (map[uint32]bool, error) {
	key := folder + "\x00" + strconv.FormatUint(uint64(expectedValidity), 10)
	if live, ok := cache[key]; ok {
		return live, nil
	}
	uidValidity, uids, err := source.MessageUIDs(ctx, folder)
	if err != nil {
		return nil, err
	}
	if uidValidity != expectedValidity {
		return map[uint32]bool{}, nil
	}
	live := make(map[uint32]bool, len(uids))
	for _, uid := range uids {
		live[uid] = true
	}
	cache[key] = live
	return live, nil
}

// queryNeedles keeps a small deterministic search budget. CJK runs use bigrams
// because PostgreSQL's simple dictionary does not segment those sentences.
func queryNeedles(query string) []string {
	out := []string{query}
	seen := map[string]bool{strings.ToLower(query): true}
	stop := map[string]bool{}
	for _, word := range strings.Fields("a an the is are was were what which who when where why how do does did can could would should will may my me our your their this that these those please tell about of to in on at for from with and or have has had much many") {
		stop[word] = true
	}
	add := func(term string) {
		term = strings.ToLower(term)
		if len(out) < 9 && !seen[term] && !stop[term] {
			seen[term] = true
			out = append(out, term)
		}
	}
	words := strings.FieldsFunc(query, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	for _, word := range words {
		runes := []rune(word)
		cjk := false
		for _, r := range runes {
			if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) {
				cjk = true
				break
			}
		}
		if cjk {
			for i := 0; i+1 < len(runes); i++ {
				add(string(runes[i : i+2]))
			}
		} else if len(runes) >= 2 {
			add(word)
		}
	}
	return out
}

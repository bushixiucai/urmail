package airetrieval

import (
	"context"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/domain/aianswer"
)

func recordingModel(calls *int, text string, citations []string) aianswer.Model {
	return func(context.Context, string, []aianswer.Source) (string, []string, error) {
		*calls++
		return text, citations, nil
	}
}

func TestAskWithoutChunksSkipsModel(t *testing.T) {
	calls := 0
	source := &FakeSource{Mail: map[string]map[uint32]string{"INBOX": {1: "weather report"}}, Validity: map[string]uint32{"INBOX": 11}}
	store := NewFakeStore()
	pipeline := Pipeline{Store: store, Source: source, Model: recordingModel(&calls, "invented", []string{"9"})}
	answer, err := pipeline.Ask(context.Background(), Principal{TenantID: 1, MailboxID: 5}, "where is the invoice")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || answer.Grounded || len(answer.Citations) != 0 {
		t.Fatalf("%+v calls=%d", answer, calls)
	}
}

func TestAnswerCitesOnlyRetrievedChunks(t *testing.T) {
	calls := 0
	source := &FakeSource{Mail: map[string]map[uint32]string{"INBOX": {7: "the invoice total is 42 euros"}}, Validity: map[string]uint32{"INBOX": 3}}
	store := NewFakeStore()
	principal := Principal{TenantID: 2, MailboxID: 9}
	indexer := Indexer{Store: store, Source: source}
	if _, err := indexer.Reindex(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	pipeline := Pipeline{Store: store, Source: source, Model: recordingModel(&calls, "Total is 42 euros.", []string{"1"})}
	answer, err := pipeline.Ask(context.Background(), principal, "invoice total")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("model calls = %d", calls)
	}
	if !answer.Grounded || len(answer.Citations) != 1 || answer.Citations[0] != "1" {
		t.Fatalf("%+v", answer)
	}
}

func TestDeletedMessageDropsFromResultsAndCitations(t *testing.T) {
	ctx := context.Background()
	calls := 0
	source := &FakeSource{Mail: map[string]map[uint32]string{"INBOX": {7: "the invoice total is 42 euros"}}, Validity: map[string]uint32{"INBOX": 3}}
	store := NewFakeStore()
	principal := Principal{TenantID: 2, MailboxID: 9}
	if _, err := (Indexer{Store: store, Source: source}).Reindex(ctx, principal); err != nil {
		t.Fatal(err)
	}
	// The model still cites the chunk, but the message was expunged meanwhile.
	delete(source.Mail["INBOX"], 7)
	pipeline := Pipeline{Store: store, Source: source, Model: recordingModel(&calls, "Total is 42 euros.", []string{"1"})}
	answer, err := pipeline.Ask(ctx, principal, "invoice total")
	if err != nil {
		t.Fatal(err)
	}
	if answer.Grounded || len(answer.Citations) != 0 {
		t.Fatalf("deleted message survived: %+v", answer)
	}
}

func TestReindexDropsExpungedMessage(t *testing.T) {
	ctx := context.Background()
	source := &FakeSource{Mail: map[string]map[uint32]string{"INBOX": {7: "invoice", 8: "receipt"}}, Validity: map[string]uint32{"INBOX": 3}}
	store := NewFakeStore()
	principal := Principal{TenantID: 2, MailboxID: 9}
	if _, err := (Indexer{Store: store, Source: source}).Reindex(ctx, principal); err != nil {
		t.Fatal(err)
	}
	delete(source.Mail["INBOX"], 7) // expunged
	if _, err := (Indexer{Store: store, Source: source}).Reindex(ctx, principal); err != nil {
		t.Fatal(err)
	}
	chunks, err := Search(ctx, store, source, principal, "invoice")
	if err != nil || len(chunks) != 0 {
		t.Fatalf("expunged message chunks survived: %+v %v", chunks, err)
	}
	chunks, err = Search(ctx, store, source, principal, "receipt")
	if err != nil || len(chunks) != 1 {
		t.Fatalf("surviving message lost: %+v %v", chunks, err)
	}
}

func TestReindexSkipsUnchangedContent(t *testing.T) {
	ctx := context.Background()
	source := &FakeSource{Mail: map[string]map[uint32]string{"INBOX": {7: "stable text"}}, Validity: map[string]uint32{"INBOX": 3}}
	store := NewFakeStore()
	principal := Principal{TenantID: 2, MailboxID: 9}
	if _, err := (Indexer{Store: store, Source: source}).Reindex(ctx, principal); err != nil {
		t.Fatal(err)
	}
	count, err := Indexer{Store: store, Source: source}.Reindex(ctx, principal)
	if err != nil || count != 0 {
		t.Fatalf("unchanged message rechunked: %d %v", count, err)
	}
}

func TestChunkTextBounded(t *testing.T) {
	text := make([]rune, 4500)
	for i := range text {
		text[i] = 'a'
	}
	chunks := ChunkText(string(text))
	if len(chunks) != 3 {
		t.Fatalf("chunk count = %d", len(chunks))
	}
	if len(ChunkText("   ")) != 0 {
		t.Fatal("blank text produced chunks")
	}
}

func TestQuoteForSourceFitsAnswerRules(t *testing.T) {
	quote := quoteForSource("line1\nline2\r\n\n" + makeLong(600))
	if len(quote) > 500 || strings.ContainsAny(quote, "\r\n") {
		t.Fatalf("bad quote: %d bytes", len(quote))
	}
}

func makeLong(n int) string {
	b := []rune{}
	for i := 0; i < n; i++ {
		b = append(b, 'x')
	}
	return string(b)
}

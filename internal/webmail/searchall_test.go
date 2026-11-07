package webmail

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeFanoutClient struct {
	folders    []Folder
	messages   map[string][]MessageSummary
	errFor     map[string]error
	queried    []ListOpts
	validities map[string]uint32
}

func (f *fakeFanoutClient) ListFolders(context.Context) ([]Folder, error) {
	return f.folders, nil
}

func (f *fakeFanoutClient) ListMessages(_ context.Context, folder string, opts ListOpts) (ListResult, error) {
	f.queried = append(f.queried, opts)
	if err, ok := f.errFor[folder]; ok {
		return ListResult{}, err
	}
	return ListResult{UIDValidity: f.validities[folder], Messages: f.messages[folder], Total: len(f.messages[folder])}, nil
}

func TestSearchAllFanoutMerge(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mid := old.Add(time.Hour)
	newVal := old.Add(2 * time.Hour)
	client := &fakeFanoutClient{
		folders: []Folder{
			{Name: "INBOX", Delimiter: "/"},
			{Name: "INBOX/Archive", Delimiter: "/"}, // child: skipped
			{Name: "Sent", Delimiter: "/"},
			{Name: "Junk", Delimiter: "/", Flags: []string{"\\Noselect"}}, // skipped
		},
		messages: map[string][]MessageSummary{
			"INBOX": {{UID: 1, Subject: "hi", Date: envelopeDate(mid), From: []Address{{Email: "a@x"}}}},
			"Sent":  {{UID: 2, Subject: "hi sent", Date: envelopeDate(newVal), From: []Address{{Email: "b@x"}}}, {UID: 3, Subject: "old", Date: envelopeDate(old)}},
		},
		errFor: map[string]error{"Junk": errors.New("broken"), "INBOX/Archive": errors.New("should not be queried")},
	}
	merged, err := SearchAll(context.Background(), client, "hi", nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 3 {
		t.Fatalf("merged %d results: %+v", len(merged), merged)
	}
	if merged[0].Folder != "Sent" || merged[0].UID != 2 {
		t.Fatalf("merge order wrong: %+v", merged[0])
	}
	if merged[2].Folder != "Sent" || merged[2].UID != 3 {
		t.Fatalf("oldest wrong: %+v", merged[2])
	}
	for _, m := range merged {
		if m.FolderEnc == "" {
			t.Fatalf("missing folder_enc: %+v", m)
		}
	}
	if len(client.queried) != 2 {
		t.Fatalf("expected 2 folder queries (top-level, non-Noselect), got %d", len(client.queried))
	}
	if client.queried[0].Query != "hi" || client.queried[0].Limit != 50 {
		t.Fatalf("query opts wrong: %+v", client.queried[0])
	}
}

func TestSearchAllSelectedFoldersAndDeadline(t *testing.T) {
	client := &fakeFanoutClient{
		folders:  []Folder{{Name: "INBOX", Delimiter: "/"}},
		messages: map[string][]MessageSummary{},
	}
	_, err := SearchAll(context.Background(), client, "x", []string{"Arch/2026", "Sent"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.queried) != 2 {
		t.Fatalf("selected scope ignored: %d", len(client.queried))
	}
	if client.queried[0].Limit != MaxSearchPerFolder {
		t.Fatalf("per-folder clamp wrong: %d", client.queried[0].Limit)
	}
	// Already-canceled context stops the loop early.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := SearchAll(ctx, client, "x", nil, 50); err != nil {
		t.Fatalf("canceled ctx should not error: %v", err)
	}
}

func TestSearchAllPreservesEachSourceUIDValidity(t *testing.T) {
	client := &fakeFanoutClient{
		folders:    []Folder{{Name: "INBOX"}, {Name: "Archive"}},
		validities: map[string]uint32{"INBOX": 7, "Archive": 19},
		messages: map[string][]MessageSummary{
			"INBOX":   {{UID: 1, UIDValidity: 999}},
			"Archive": {{UID: 1, UIDValidity: 999}},
		},
	}
	rows, err := SearchAll(context.Background(), client, "same uid", nil, 50)
	if err != nil || len(rows) != 2 {
		t.Fatalf("search: rows=%+v err=%v", rows, err)
	}
	for _, row := range rows {
		if row.UIDValidity != client.validities[row.Folder] || row.FolderEnc != EncodeFolderName(row.Folder) {
			t.Fatalf("source identity replaced with another mailbox: %+v", row)
		}
	}
}

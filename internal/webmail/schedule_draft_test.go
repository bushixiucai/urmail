package webmail

import (
	"context"
	"testing"
)

type fakeDraftClient struct {
	msg  *Message
	atts map[string][]byte
}

func (f *fakeDraftClient) ListFolders(context.Context) ([]Folder, error) { return nil, nil }
func (f *fakeDraftClient) ListMessages(context.Context, string, ListOpts) (ListResult, error) {
	return ListResult{}, nil
}
func (f *fakeDraftClient) GetMessage(_ context.Context, _ string, uid uint32) (*Message, error) {
	if f.msg == nil || f.msg.UID != uid {
		return nil, ErrDialFailed
	}
	return f.msg, nil
}
func (f *fakeDraftClient) GetAttachment(_ context.Context, _ string, uid uint32, partID string) (string, string, []byte, error) {
	if f.msg == nil || f.msg.UID != uid {
		return "", "", nil, ErrDialFailed
	}
	data, ok := f.atts[partID]
	if !ok {
		return "", "", nil, ErrDialFailed
	}
	for _, a := range f.msg.Attachments {
		if a.PartID == partID {
			return a.Filename, a.ContentType, data, nil
		}
	}
	return "", "", nil, ErrDialFailed
}
func (f *fakeDraftClient) SetFlags(context.Context, string, []uint32, []string, []string) error {
	return nil
}
func (f *fakeDraftClient) SaveSentCopy(context.Context, []byte) (DraftSaveResult, error) {
	return DraftSaveResult{}, nil
}
func (f *fakeDraftClient) MoveToTrash(context.Context, string, []uint32) (string, error) {
	return "Trash", nil
}
func (f *fakeDraftClient) MoveMessages(context.Context, string, []uint32, string) ([]uint32, error) {
	return nil, nil
}
func (f *fakeDraftClient) DeleteMessages(context.Context, string, []uint32, bool) error {
	return nil
}
func (f *fakeDraftClient) EnsureDraftsFolder(context.Context) (string, error) { return "Drafts", nil }
func (f *fakeDraftClient) SaveDraft(context.Context, string, Outgoing, uint32, uint32) (DraftSaveResult, error) {
	return DraftSaveResult{}, nil
}
func (f *fakeDraftClient) FolderUIDValidity(context.Context, string) (uint32, error) { return 1, nil }
func (f *fakeDraftClient) Close() error                                              { return nil }

func TestOutgoingFromDraft(t *testing.T) {
	client := &fakeDraftClient{
		msg: &Message{
			MessageSummary: MessageSummary{UID: 9, Subject: "Later"},
			To:             []Address{{Email: "bob@example.test"}},
			Bcc:            []Address{{Email: "secret@example.test"}},
			Text:           "body",
			Attachments: []AttachmentMeta{
				{PartID: "2", Filename: "a.txt", ContentType: "text/plain", Size: 3},
			},
		},
		atts: map[string][]byte{"2": []byte("hi!")},
	}
	out, err := OutgoingFromDraft(context.Background(), client, "Drafts", 9)
	if err != nil {
		t.Fatal(err)
	}
	if out.Subject != "Later" || out.Text != "body" {
		t.Fatalf("body=%#v", out)
	}
	if len(out.To) != 1 || out.To[0] != "bob@example.test" {
		t.Fatalf("to=%v", out.To)
	}
	if len(out.Bcc) != 1 || out.Bcc[0] != "secret@example.test" {
		t.Fatalf("bcc=%v", out.Bcc)
	}
	if len(out.Attachments) != 1 || string(out.Attachments[0].Content) != "hi!" {
		t.Fatalf("atts=%#v", out.Attachments)
	}
}

func TestOutgoingFromDraftNoRecipients(t *testing.T) {
	client := &fakeDraftClient{
		msg: &Message{MessageSummary: MessageSummary{UID: 1, Subject: "x"}, Text: "y"},
	}
	if _, err := OutgoingFromDraft(context.Background(), client, "Drafts", 1); err == nil {
		t.Fatal("expected error")
	}
}

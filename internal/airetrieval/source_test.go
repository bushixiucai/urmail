package airetrieval

import (
	"context"
	"reflect"
	"testing"

	"github.com/bushixiucai/urmail/internal/webmail"
)

type folderOnlyClient struct {
	webmail.Client
	folders []webmail.Folder
}

func (f folderOnlyClient) ListFolders(context.Context) ([]webmail.Folder, error) {
	return f.folders, nil
}

func TestWebmailSourceSkipsNonselectableNamespaceParents(t *testing.T) {
	source := WebmailSource{Client: folderOnlyClient{folders: []webmail.Folder{
		{Name: "namespace", Flags: []string{`\Noselect`, `\HasChildren`}},
		{Name: "namespace.child", Flags: []string{`\HasNoChildren`}},
		{Name: "missing", Flags: []string{`\nonexistent`}},
		{Name: "mixed-case", Flags: []string{`\nOsElEcT`}},
		{Name: "INBOX", Flags: []string{`\HasNoChildren`}},
		{Name: ""},
	}}}
	actual, err := source.Folders(t.Context())
	if err != nil || !reflect.DeepEqual(actual, []string{"namespace.child", "INBOX"}) {
		t.Fatalf("selectable folders=%v err=%v", actual, err)
	}
}

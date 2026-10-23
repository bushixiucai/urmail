package agentd

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRecallExpungesOnlyExactHeaderAndGUIDUID(t *testing.T) {
	const guid = "0123456789abcdef0123456789abcdef"
	var commands [][]string
	m := &ApplyManager{run: func(_ context.Context, path string, args ...string) error {
		commands = append(commands, append([]string{path}, args...))
		return nil
	}}
	m.output = func(_ context.Context, args ...string) ([]byte, error) {
		switch args[2] {
		case "search":
			return []byte(`[{"mailbox-guid":"` + guid + `","uid":10},{"mailbox-guid":"` + guid + `","uid":"11"}]`), nil
		case "fetch":
			id := "<wanted@example.test>"
			if args[len(args)-1] == "10" {
				id = "prefix " + id + " suffix"
			}
			return json.Marshal([]map[string]string{{"hdr.message-id": id}})
		default:
			t.Fatalf("unexpected %v", args)
			return nil, nil
		}
	}
	found, err := m.expungeExact(context.Background(), "alice@example.test", "<wanted@example.test>")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	want := [][]string{{"/usr/bin/doveadm", "expunge", "-u", "alice@example.test", "mailbox-guid", guid, "uid", "11"}}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands=%v", commands)
	}
}

func TestRecallRejectsUnsafeSelectorsAndOutput(t *testing.T) {
	for _, output := range []string{`[{"mailbox-guid":"../../x","uid":1}]`, `[{"mailbox-guid":"0123456789abcdef0123456789abcdef","uid":0}]`, `not-json`} {
		m := &ApplyManager{run: func(context.Context, string, ...string) error { t.Fatal("unsafe expunge executed"); return nil }, output: func(context.Context, ...string) ([]byte, error) { return []byte(output), nil }}
		if _, err := m.expungeExact(context.Background(), "alice@example.test", "<wanted@example.test>"); err == nil {
			t.Errorf("accepted %q", output)
		}
	}
	for _, id := range []string{"<id>\nInjected", "arbitrary", strings.Repeat("x", 999)} {
		m := &ApplyManager{output: func(context.Context, ...string) ([]byte, error) { t.Fatal("unsafe query executed"); return nil, nil }}
		if _, err := m.expungeExact(context.Background(), "alice@example.test", id); err == nil {
			t.Errorf("accepted unsafe identity")
		}
	}
	var b boundedOutput
	if _, err := b.Write(make([]byte, 4<<20)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("x")); err == nil {
		t.Fatal("unbounded output")
	}
}

package webmail

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestParseLIST(t *testing.T) {
	cases := []struct {
		line string
		want Folder
	}{
		{
			line: `* LIST (\HasNoChildren) "/" INBOX`,
			want: Folder{Name: "INBOX", Delimiter: "/", Flags: []string{`\HasNoChildren`}},
		},
		{
			line: `* LIST () "/" "Sent Messages"`,
			want: Folder{Name: "Sent Messages", Delimiter: "/", Flags: nil},
		},
		{
			line: `* LIST (\Noselect \HasChildren) "." "INBOX.Foo"`,
			want: Folder{Name: "INBOX.Foo", Delimiter: ".", Flags: []string{`\Noselect`, `\HasChildren`}},
		},
	}
	for _, tc := range cases {
		got, ok := parseLIST(tc.line)
		if !ok {
			t.Fatalf("parseLIST(%q) failed", tc.line)
		}
		if got.Name != tc.want.Name || got.Delimiter != tc.want.Delimiter {
			t.Fatalf("parseLIST(%q) = %+v, want %+v", tc.line, got, tc.want)
		}
		if strings.Join(got.Flags, " ") != strings.Join(tc.want.Flags, " ") {
			t.Fatalf("flags = %v, want %v", got.Flags, tc.want.Flags)
		}
	}
}

func TestNetDialerListFolders(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	errCh := make(chan error, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			errCh <- acceptErr
			return
		}
		defer conn.Close()
		w := bufio.NewWriter(conn)
		r := bufio.NewReader(conn)
		_, _ = io.WriteString(w, "* OK Dovecot ready\r\n")
		_ = w.Flush()
		line, _ := r.ReadString('\n')
		if !strings.Contains(line, `LOGIN "user@example.test"`) {
			errCh <- io.ErrUnexpectedEOF
			return
		}
		tag := strings.Fields(line)[0]
		_, _ = io.WriteString(w, tag+" OK Logged in\r\n")
		_ = w.Flush()
		line, _ = r.ReadString('\n')
		tag = strings.Fields(line)[0]
		_, _ = io.WriteString(w, `* LIST (\HasNoChildren) "/" INBOX`+"\r\n")
		_, _ = io.WriteString(w, `* LIST () "/" "Archive"`+"\r\n")
		_, _ = io.WriteString(w, tag+" OK List completed\r\n")
		_ = w.Flush()
		errCh <- nil
	}()

	dialer := NetDialer{Addr: ln.Addr().String(), Timeout: 2 * time.Second}
	client, err := dialer.Dial(context.Background(), "user@example.test", "secret")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	folders, err := client.ListFolders(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(folders) != 2 || folders[0].Name != "INBOX" || folders[1].Name != "Archive" {
		t.Fatalf("unexpected folders %#v", folders)
	}
	decoded, decErr := DecodeFolderName(folders[0].NameEnc)
	if decErr != nil || decoded != "INBOX" {
		t.Fatalf("name_enc missing: %#v", folders[0])
	}
	if err := <-errCh; err != nil {
		t.Fatalf("server: %v", err)
	}
}

func TestNetDialerListAndGetMessage(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	env := `("17-Oct-2026 10:00:00 +0000" "Subj" (("Alice" NIL "alice" "example.test")) NIL NIL NIL NIL NIL NIL NIL)`
	body := "Subject: Subj\r\nContent-Type: text/plain\r\n\r\nbody-text"
	go func() {
		conn, _ := ln.Accept()
		defer conn.Close()
		w := bufio.NewWriter(conn)
		r := bufio.NewReader(conn)
		_, _ = io.WriteString(w, "* OK ready\r\n")
		_ = w.Flush()
		line, _ := r.ReadString('\n')
		tag := strings.Fields(line)[0]
		_, _ = io.WriteString(w, tag+" OK\r\n")
		_ = w.Flush()
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			tag = strings.Fields(line)[0]
			switch {
			case strings.Contains(line, "SELECT"):
				_, _ = io.WriteString(w, tag+" OK [READ-WRITE]\r\n")
			case strings.Contains(line, "UID SEARCH"):
				_, _ = io.WriteString(w, "* SEARCH 1 2\r\n"+tag+" OK\r\n")
			case strings.Contains(line, "UID FETCH") && strings.Contains(line, "ENVELOPE"):
				_, _ = io.WriteString(w, `* 1 FETCH (UID 2 FLAGS (\Seen) RFC822.SIZE 12 ENVELOPE `+env+")\r\n")
				_, _ = io.WriteString(w, `* 2 FETCH (UID 1 FLAGS () RFC822.SIZE 9 ENVELOPE `+env+")\r\n")
				_, _ = io.WriteString(w, tag+" OK\r\n")
			case strings.Contains(line, "BODY.PEEK"):
				_, _ = fmt.Fprintf(w, "* 1 FETCH (BODY[] {%d}\r\n", len(body))
				_, _ = io.WriteString(w, body)
				_, _ = io.WriteString(w, ")\r\n"+tag+" OK\r\n")
			case strings.Contains(line, "LOGOUT"):
				_, _ = io.WriteString(w, tag+" OK\r\n")
				_ = w.Flush()
				return
			default:
				_, _ = io.WriteString(w, tag+" OK\r\n")
			}
			_ = w.Flush()
		}
	}()

	dialer := NetDialer{Addr: ln.Addr().String(), Timeout: 2 * time.Second}
	client, err := dialer.Dial(context.Background(), "user@example.test", "secret")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	listed, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Limit: 10})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if listed.Total != 2 || len(listed.Messages) != 2 || listed.Messages[0].UID != 2 {
		t.Fatalf("listed=%#v", listed)
	}
	msg, err := client.GetMessage(context.Background(), "INBOX", 2)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if msg.Text != "body-text" || msg.Subject != "Subj" {
		t.Fatalf("msg=%#v", msg)
	}
}

func TestNetDialerListMessagesSearch(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	env := `("17-Oct-2026 10:00:00 +0000" "Subj" (("Alice" NIL "alice" "example.test")) NIL NIL NIL NIL NIL NIL NIL)`
	searchLine := make(chan string, 1)
	go func() {
		conn, _ := ln.Accept()
		defer conn.Close()
		w := bufio.NewWriter(conn)
		r := bufio.NewReader(conn)
		_, _ = io.WriteString(w, "* OK ready\r\n")
		_ = w.Flush()
		line, _ := r.ReadString('\n')
		tag := strings.Fields(line)[0]
		_, _ = io.WriteString(w, tag+" OK\r\n")
		_ = w.Flush()
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			tag = strings.Fields(line)[0]
			switch {
			case strings.Contains(line, "SELECT"):
				_, _ = io.WriteString(w, tag+" OK [READ-WRITE]\r\n")
			case strings.Contains(line, "UID SEARCH"):
				searchLine <- strings.TrimSpace(line)
				_, _ = io.WriteString(w, "* SEARCH 9\r\n"+tag+" OK\r\n")
			case strings.Contains(line, "UID FETCH") && strings.Contains(line, "ENVELOPE"):
				_, _ = io.WriteString(w, `* 1 FETCH (UID 9 FLAGS () RFC822.SIZE 3 ENVELOPE `+env+")\r\n")
				_, _ = io.WriteString(w, tag+" OK\r\n")
			case strings.Contains(line, "LOGOUT"):
				_, _ = io.WriteString(w, tag+" OK\r\n")
				_ = w.Flush()
				return
			default:
				_, _ = io.WriteString(w, tag+" OK\r\n")
			}
			_ = w.Flush()
		}
	}()

	client, err := (NetDialer{Addr: ln.Addr().String(), Timeout: 2 * time.Second}).Dial(context.Background(), "u@e.test", "pw")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	listed, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Limit: 10, Query: "subject:Hi from:a is:unseen"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if listed.Total != 1 || len(listed.Messages) != 1 || listed.Messages[0].UID != 9 {
		t.Fatalf("listed=%#v", listed)
	}
	cmd := <-searchLine
	for _, want := range []string{"UID SEARCH", "UNSEEN", `SUBJECT "Hi"`, `FROM "a"`} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("missing %q in %q", want, cmd)
		}
	}
	if strings.Contains(cmd, "ALL") {
		t.Fatalf("unexpected ALL in %q", cmd)
	}
}

func TestNetDialerFlagsMoveDelete(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	seen := make(chan string, 8)
	go func() {
		conn, _ := ln.Accept()
		defer conn.Close()
		w := bufio.NewWriter(conn)
		r := bufio.NewReader(conn)
		_, _ = io.WriteString(w, "* OK ready\r\n")
		_ = w.Flush()
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			tag := strings.Fields(line)[0]
			seen <- strings.TrimSpace(line)
			_, _ = io.WriteString(w, tag+" OK\r\n")
			_ = w.Flush()
			if strings.Contains(line, "LOGOUT") {
				return
			}
		}
	}()

	client, err := (NetDialer{Addr: ln.Addr().String(), Timeout: 2 * time.Second}).Dial(context.Background(), "u@e.test", "pw")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	if err := client.SetFlags(context.Background(), "INBOX", []uint32{3}, []string{`\Seen`}, nil); err != nil {
		t.Fatal(err)
	}
	destinations, err := client.MoveMessages(context.Background(), "INBOX", []uint32{3}, "Archive")
	if err != nil {
		t.Fatal(err)
	}
	// The fake server never emits COPYUID: move succeeds but the mapping is unknown.
	if destinations.DestinationUIDs != nil || destinations.DestinationUIDValidity != 0 {
		t.Fatalf("expected nil destinations without COPYUID, got %v", destinations)
	}
	if err := client.DeleteMessages(context.Background(), "INBOX", []uint32{3}, true); err != nil {
		t.Fatal(err)
	}
	var cmds []string
	deadline := time.After(2 * time.Second)
	for {
		select {
		case c := <-seen:
			cmds = append(cmds, c)
			if strings.Contains(c, "UID EXPUNGE") {
				joined := strings.Join(cmds, "\n")
				for _, want := range []string{"LOGIN", "SELECT", "UID STORE", "UID MOVE", "UID EXPUNGE"} {
					if !strings.Contains(joined, want) {
						t.Fatalf("missing %s in %s", want, joined)
					}
				}
				return
			}
		case <-deadline:
			t.Fatalf("cmds=%v", cmds)
		}
	}
}

func TestMemoryVault(t *testing.T) {
	v := NewMemoryVault()
	v.Put("abc", "secret")
	got, ok := v.Get("abc")
	if !ok || got != "secret" {
		t.Fatalf("get = %q %v", got, ok)
	}
	v.Delete("abc")
	if _, ok = v.Get("abc"); ok {
		t.Fatal("expected delete")
	}
}

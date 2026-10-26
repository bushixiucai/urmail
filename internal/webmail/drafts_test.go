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

func TestResolveDraftsFolder(t *testing.T) {
	t.Parallel()
	special := []Folder{
		{Name: "INBOX", Flags: []string{`\HasNoChildren`}},
		{Name: "MyDrafts", Flags: []string{`\Drafts`, `\HasNoChildren`}},
		{Name: "Drafts", Flags: []string{`\HasNoChildren`}},
	}
	if got := ResolveDraftsFolder(special); got != "MyDrafts" {
		t.Fatalf("prefer \\Drafts: got %q", got)
	}
	byName := []Folder{
		{Name: "INBOX"},
		{Name: "drafts", Flags: []string{`\HasNoChildren`}},
	}
	if got := ResolveDraftsFolder(byName); got != "drafts" {
		t.Fatalf("prefer name: got %q", got)
	}
	if got := ResolveDraftsFolder([]Folder{{Name: "INBOX"}}); got != "" {
		t.Fatalf("empty want, got %q", got)
	}
	if !IsDraftsFolder(Folder{Name: "x", Flags: []string{`\Drafts`}}) {
		t.Fatal("flag should match")
	}
	if !IsDraftsFolder(Folder{Name: "Drafts"}) {
		t.Fatal("name should match")
	}
	if IsDraftsFolder(Folder{Name: "INBOX"}) {
		t.Fatal("inbox is not drafts")
	}
}

func TestParseAPPENDUID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		line string
		uid  uint32
	}{
		{`A3 OK [APPENDUID 38505 3955] Append completed`, 3955},
		{`A3 OK Append completed`, 0},
		{`A9 OK [APPENDUID 1 42]`, 42},
	}
	for _, tc := range cases {
		if _, got := parseAPPENDUID(tc.line); got != tc.uid {
			t.Fatalf("parseAPPENDUID(%q)=%d want %d", tc.line, got, tc.uid)
		}
	}
}

func TestAppendDraftLiteral(t *testing.T) {
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
		_, _ = io.WriteString(w, "* OK ready\r\n")
		_ = w.Flush()
		line, _ := r.ReadString('\n') // LOGIN
		tag := strings.Fields(line)[0]
		_, _ = io.WriteString(w, tag+" OK\r\n")
		_ = w.Flush()

		line, _ = r.ReadString('\n') // LIST
		tag = strings.Fields(line)[0]
		_, _ = io.WriteString(w, `* LIST (\HasNoChildren) "/" INBOX`+"\r\n")
		_, _ = io.WriteString(w, tag+" OK\r\n")
		_ = w.Flush()

		line, _ = r.ReadString('\n') // CREATE Drafts
		tag = strings.Fields(line)[0]
		if !strings.Contains(line, `CREATE "Drafts"`) {
			errCh <- fmt.Errorf("expected CREATE Drafts, got %q", line)
			return
		}
		_, _ = io.WriteString(w, tag+" OK\r\n")
		_ = w.Flush()

		line, _ = r.ReadString('\n') // LIST again
		tag = strings.Fields(line)[0]
		_, _ = io.WriteString(w, `* LIST (\HasNoChildren) "/" INBOX`+"\r\n")
		_, _ = io.WriteString(w, `* LIST (\Drafts \HasNoChildren) "/" Drafts`+"\r\n")
		_, _ = io.WriteString(w, tag+" OK\r\n")
		_ = w.Flush()

		line, _ = r.ReadString('\n') // APPEND
		tag = strings.Fields(line)[0]
		if !strings.Contains(line, `APPEND "Drafts" (\Draft) {`) {
			errCh <- fmt.Errorf("bad APPEND: %q", line)
			return
		}
		size, ok := trailingLiteralSize(strings.TrimRight(line, "\r\n"))
		if !ok {
			errCh <- fmt.Errorf("no literal size: %q", line)
			return
		}
		_, _ = io.WriteString(w, "+ go ahead\r\n")
		_ = w.Flush()
		buf := make([]byte, size)
		if _, err = io.ReadFull(r, buf); err != nil {
			errCh <- err
			return
		}
		if !strings.Contains(string(buf), "Subject: Draft subject") {
			errCh <- fmt.Errorf("payload missing subject: %q", string(buf))
			return
		}
		_, _ = io.WriteString(w, tag+" OK [APPENDUID 9 77] Append completed\r\n")
		_ = w.Flush()
		errCh <- nil
	}()

	dialer := NetDialer{Addr: ln.Addr().String(), Timeout: 2 * time.Second}
	client, err := dialer.Dial(context.Background(), "user@example.test", "secret")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	res, err := client.SaveDraft(context.Background(), "user@example.test", Outgoing{
		To:      []string{"bob@example.test"},
		Subject: "Draft subject",
		Text:    "body",
	}, 0, 0)
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if res.Folder != "Drafts" || res.UID != 77 || res.UIDValidity != 9 || res.NameEnc == "" {
		t.Fatalf("result=%+v", res)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("server: %v", err)
	}
}

func TestBuildMIMEIncludesAttachments(t *testing.T) {
	t.Parallel()
	raw := BuildMIME("alice@example.test", Outgoing{
		To:      []string{"bob@example.test"},
		Subject: "with file",
		Text:    "hi",
		Attachments: []OutgoingAttachment{{
			Filename:    "a.txt",
			ContentType: "text/plain",
			Content:     []byte("hello"),
		}},
	})
	if !strings.Contains(raw, "filename=\"a.txt\"") || !strings.Contains(raw, "multipart/mixed") {
		t.Fatalf("mime=%q", raw)
	}
}

package webmail

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// previewServer serves a small mailbox for the batched snippet tests. It
// answers every command with a tagged OK, records the commands it receives,
// honors the partial byte count of BODY.PEEK[]<0.N> requests and simulates a
// fixed per-command latency.
func startPreviewServer(t *testing.T, searchUIDs []uint32, bodies map[uint32][]byte, delay time.Duration) (addr string, cmds chan string) {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	cmds = make(chan string, 64)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
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
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			tag := fields[0]
			select {
			case cmds <- strings.TrimSpace(line):
			default:
			}
			if delay > 0 {
				time.Sleep(delay)
			}
			switch {
			case strings.Contains(line, "SELECT"):
				_, _ = io.WriteString(w, "* OK [UIDVALIDITY 7] selected\r\n"+tag+" OK select done\r\n")
			case strings.Contains(line, "UID SEARCH"):
				_, _ = io.WriteString(w, "* SEARCH "+itoaJoin(searchUIDs)+"\r\n"+tag+" OK search done\r\n")
			case strings.Contains(line, "BODY.PEEK"):
				writePreviewFetch(w, line, tag, bodies)
			case strings.Contains(line, "UID FETCH"):
				for _, uid := range searchUIDs {
					_, _ = fmt.Fprintf(w, "* 1 FETCH (UID %d FLAGS (\\Seen) RFC822.SIZE %d ENVELOPE (NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL))\r\n", uid, len(bodies[uid]))
				}
				_, _ = io.WriteString(w, tag+" OK fetch done\r\n")
			default:
				_, _ = io.WriteString(w, tag+" OK\r\n")
			}
			_ = w.Flush()
			if strings.Contains(line, "LOGOUT") {
				return
			}
		}
	}()
	return ln.Addr().String(), cmds
}

// writePreviewFetch answers a batched split preview fetch: every UID of the
// requested set gets a HEADER.FIELDS literal carrying only the requested
// fields plus a TEXT literal truncated to the requested partial byte count,
// like a compliant server.
func writePreviewFetch(w *bufio.Writer, command, tag string, bodies map[uint32][]byte) {
	uids := requestUIDSet(command)
	perMessage := requestedPartialBytes(command)
	wanted := requestedHeaderFields(command)
	for _, uid := range uids {
		header, text, _ := bytes.Cut(bodies[uid], []byte("\r\n\r\n"))
		header = filterHeaderFields(header, wanted)
		if perMessage > 0 && perMessage < len(text) {
			text = text[:perMessage]
		}
		_, _ = fmt.Fprintf(w, "* 1 FETCH (UID %d BODY[HEADER.FIELDS (CONTENT-TYPE CONTENT-TRANSFER-ENCODING)] {%d}\r\n", uid, len(header))
		_, _ = w.Write(header)
		_, _ = fmt.Fprintf(w, " BODY[TEXT]<0> {%d}\r\n", len(text))
		_, _ = w.Write(text)
		_, _ = io.WriteString(w, ")\r\n")
	}
	_, _ = io.WriteString(w, tag+" OK [PARTIAL] fetch done\r\n")
}

// requestedHeaderFields parses the field list of a HEADER.FIELDS request.
func requestedHeaderFields(command string) map[string]bool {
	wanted := map[string]bool{}
	start := strings.Index(command, "HEADER.FIELDS (")
	if start < 0 {
		return wanted
	}
	rest := command[start+len("HEADER.FIELDS ("):]
	end := strings.IndexByte(rest, ')')
	if end < 0 {
		return wanted
	}
	for _, field := range strings.Fields(rest[:end]) {
		wanted[strings.ToLower(field)] = true
	}
	return wanted
}

// filterHeaderFields keeps only the wanted fields of a raw header block,
// each line CRLF-terminated like a real HEADER.FIELDS response.
func filterHeaderFields(header []byte, wanted map[string]bool) []byte {
	var out []byte
	for _, line := range strings.Split(string(header), "\r\n") {
		idx := strings.IndexByte(line, ':')
		if idx <= 0 || !wanted[strings.ToLower(strings.TrimSpace(line[:idx]))] {
			continue
		}
		out = append(out, line...)
		out = append(out, '\r', '\n')
	}
	return out
}

// requestUIDSet parses the UID set of a "UID FETCH <set> ..." command line.
func requestUIDSet(command string) []uint32 {
	fields := strings.Fields(command)
	for i, field := range fields {
		if !strings.EqualFold(field, "FETCH") {
			continue
		}
		if i+1 >= len(fields) {
			return nil
		}
		var uids []uint32
		for _, part := range strings.Split(fields[i+1], ",") {
			uid, err := strconv.ParseUint(part, 10, 32)
			if err == nil {
				uids = append(uids, uint32(uid))
			}
		}
		return uids
	}
	return nil
}

// requestedPartialBytes parses the <0.N> partial byte count of a BODY.PEEK[]
// request.
func requestedPartialBytes(command string) int {
	start := strings.Index(command, "<0.")
	if start < 0 {
		return 0
	}
	end := strings.IndexByte(command[start:], '>')
	if end < 0 {
		return 0
	}
	n, err := strconv.Atoi(command[start+3 : start+end])
	if err != nil {
		return 0
	}
	return n
}

func previewBodies() map[uint32][]byte {
	return map[uint32][]byte{
		1: []byte("Subject: one\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHello one\r\nSecond line of one\r\n"),
		2: []byte("Subject: two\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>HTML two</p><img src=\"cid:logo@example.test\" alt=\"logo\">\r\n"),
		3: []byte("Subject: three\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nOn Mon, 12 Oct 2026 at 10:00:00, Bob <bob@example.test> wrote:\r\n> old text\r\nReply line of three\r\n"),
	}
}

func drainCommands(cmds chan string, deadline time.Duration) []string {
	var collected []string
	timer := time.After(deadline)
	for {
		select {
		case cmd := <-cmds:
			collected = append(collected, cmd)
			if strings.Contains(cmd, "LOGOUT") {
				return collected
			}
		case <-timer:
			return collected
		}
	}
}

func TestListMessagesFillsPreviewWithSingleBulkFetch(t *testing.T) {
	addr, cmds := startPreviewServer(t, []uint32{1, 2, 3}, previewBodies(), 0)
	client := dialTestClient(t, addr)
	result, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Preview: true, Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := map[uint32]string{
		1: "Hello one Second line of one",
		2: "HTML two",
		3: "Reply line of three",
	}
	if len(result.Messages) != 3 {
		t.Fatalf("messages=%d want 3", len(result.Messages))
	}
	for _, message := range result.Messages {
		if message.Preview != want[message.UID] {
			t.Fatalf("uid %d preview = %q, want %q", message.UID, message.Preview, want[message.UID])
		}
	}

	// Exactly one bulk fetch served all three snippets in a single command.
	collected := drainCommands(cmds, 500*time.Millisecond)
	var bulk []string
	for _, cmd := range collected {
		if strings.Contains(cmd, "BODY.PEEK") {
			bulk = append(bulk, cmd)
		}
	}
	if len(bulk) != 1 {
		t.Fatalf("expected one bulk preview fetch, got %d: %v", len(bulk), collected)
	}
	for _, uid := range []uint32{1, 2, 3} {
		if !strings.Contains(bulk[0], strconv.FormatUint(uint64(uid), 10)) {
			t.Fatalf("bulk fetch %q misses uid %d", bulk[0], uid)
		}
	}
}

func TestListMessagesWithoutPreviewSendsNoBulkFetch(t *testing.T) {
	addr, cmds := startPreviewServer(t, []uint32{1, 2}, previewBodies(), 0)
	client := dialTestClient(t, addr)
	if _, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Limit: 10}); err != nil {
		t.Fatalf("list: %v", err)
	}
	collected := drainCommands(cmds, 300*time.Millisecond)
	for _, cmd := range collected {
		if strings.Contains(cmd, "BODY.PEEK") {
			t.Fatalf("unexpected preview fetch: %q", cmd)
		}
	}
}

func TestListMessagesPreviewByteBudgets(t *testing.T) {
	// One long body: the fetched chunk is bounded and the snippet truncates.
	longBody := []byte("Subject: long\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + strings.Repeat("verylongword ", 900))
	addr, _ := startPreviewServer(t, []uint32{9}, map[uint32][]byte{9: longBody}, 0)
	client := dialTestClient(t, addr)
	result, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Preview: true, Limit: 1})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("messages=%d", len(result.Messages))
	}
	preview := result.Messages[0].Preview
	if runes := utf8.RuneCountInString(preview); runes > previewMaxChars {
		t.Fatalf("preview length %d exceeds %d", runes, previewMaxChars)
	}
	if !strings.HasSuffix(preview, "…") {
		t.Fatalf("preview %q must be truncated with an ellipsis", preview)
	}

	// A full page shrinks the per-message budget so the page stays bounded.
	uids := make([]uint32, 0, 100)
	bodies := map[uint32][]byte{}
	for uid := 1; uid <= 100; uid++ {
		uids = append(uids, uint32(uid))
		bodies[uint32(uid)] = []byte("Subject: bulk\r\nContent-Type: text/plain\r\n\r\nbody\r\n")
	}
	addr, cmds := startPreviewServer(t, uids, bodies, 0)
	client = dialTestClient(t, addr)
	if _, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Preview: true, Limit: 100}); err != nil {
		t.Fatalf("list page: %v", err)
	}
	collected := drainCommands(cmds, 500*time.Millisecond)
	bulk := 0
	wantBudget := previewPageBytes / MaxPageSize
	for _, cmd := range collected {
		if !strings.Contains(cmd, "BODY.PEEK") {
			continue
		}
		bulk++
		if !strings.Contains(cmd, fmt.Sprintf("<0.%d>", wantBudget)) {
			t.Fatalf("page budget command %q must request %d bytes per message", cmd, wantBudget)
		}
	}
	if bulk != 1 {
		t.Fatalf("expected one bulk fetch for the full page, got %d", bulk)
	}
}

func TestListMessagesPreviewFetchFailureDegradesGracefully(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
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
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			tag := fields[0]
			switch {
			case strings.Contains(line, "SELECT"):
				_, _ = io.WriteString(w, "* OK [UIDVALIDITY 7] selected\r\n"+tag+" OK\r\n")
			case strings.Contains(line, "UID SEARCH"):
				_, _ = io.WriteString(w, "* SEARCH 4\r\n"+tag+" OK\r\n")
			case strings.Contains(line, "BODY.PEEK"):
				_, _ = io.WriteString(w, tag+" NO server refuses partial fetch\r\n")
			case strings.Contains(line, "UID FETCH"):
				_, _ = io.WriteString(w, "* 1 FETCH (UID 4 FLAGS () RFC822.SIZE 10 ENVELOPE (NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL))\r\n"+tag+" OK\r\n")
			default:
				_, _ = io.WriteString(w, tag+" OK\r\n")
			}
			_ = w.Flush()
			if strings.Contains(line, "LOGOUT") {
				return
			}
		}
	}()

	client := dialTestClient(t, ln.Addr().String())
	result, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Preview: true, Limit: 1})
	if err != nil {
		t.Fatalf("a failed snippet fetch must not fail the listing: %v", err)
	}
	if len(result.Messages) != 1 || result.Messages[0].Preview != "" {
		t.Fatalf("degraded preview = %#v", result.Messages)
	}
}

func TestParseFetchUIDVariants(t *testing.T) {
	cases := []struct {
		line string
		uid  uint32
		ok   bool
	}{
		{line: "(UID 42 BODY[]<0> {10}", uid: 42, ok: true},
		{line: "(BODY[]<0> {10} UID 7)", uid: 7, ok: true},
		{line: "(BODY[]<0> NIL)", uid: 0, ok: false},
	}
	for _, tc := range cases {
		uid, ok := parseFetchUID(tc.line)
		if ok != tc.ok || uid != tc.uid {
			t.Fatalf("parseFetchUID(%q) = %d %v, want %d %v", tc.line, uid, ok, tc.uid, tc.ok)
		}
	}
}

// TestListMessagesPreviewIgnoresOversizedHeaders is the v0.6.10-hotfix-2
// DEF-1 regression: production header chains (DKIM signatures, mailing-list
// stamps) exceed the whole preview byte budget, so a head-only partial fetch
// returned zero body bytes and every preview stayed empty. The split fetch
// must skip the headers entirely, snippet from the TEXT literal, and keep
// the single batched command shape.
func TestListMessagesPreviewIgnoresOversizedHeaders(t *testing.T) {
	dkim := strings.Repeat("DKIM-Signature: v=1; a=rsa-sha256; b="+strings.Repeat("x", 90)+"\r\n", 22)
	bodies := map[uint32][]byte{
		7: []byte(dkim + "Content-Type: text/plain; charset=utf-8\r\n\r\nVisible body text after huge headers\r\n"),
	}
	addr, cmds := startPreviewServer(t, []uint32{7}, bodies, 0)
	client := dialTestClient(t, addr)
	result, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Preview: true, Limit: 1})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("messages=%d", len(result.Messages))
	}
	if got := result.Messages[0].Preview; got != "Visible body text after huge headers" {
		t.Fatalf("preview = %q, want the body text behind the oversized headers", got)
	}

	// The bulk command must still be one command, request only the two
	// snippet headers, and partial-fetch the TEXT section.
	collected := drainCommands(cmds, 500*time.Millisecond)
	bulk := 0
	for _, cmd := range collected {
		if !strings.Contains(cmd, "BODY.PEEK") {
			continue
		}
		bulk++
		if !strings.Contains(cmd, "HEADER.FIELDS (CONTENT-TYPE CONTENT-TRANSFER-ENCODING)") {
			t.Fatalf("bulk command %q must request only the snippet header fields", cmd)
		}
		if !strings.Contains(cmd, fmt.Sprintf("BODY.PEEK[TEXT]<0.%d>", previewFetchBytes)) {
			t.Fatalf("bulk command %q must partial-fetch TEXT with the %d byte budget", cmd, previewFetchBytes)
		}
		if strings.Contains(cmd, "BODY.PEEK[]") {
			t.Fatalf("bulk command %q must not fetch the raw head anymore", cmd)
		}
	}
	if bulk != 1 {
		t.Fatalf("expected one bulk preview fetch, got %d: %v", bulk, collected)
	}
}

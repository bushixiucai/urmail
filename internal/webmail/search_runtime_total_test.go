package webmail

import (
	"bufio"
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func itoaJoin(uids []uint32) string {
	parts := make([]string, len(uids))
	for i, u := range uids {
		parts[i] = strconv.FormatUint(uint64(u), 10)
	}
	return strings.Join(parts, " ")
}

// startSearchServer answers UID SEARCH with the given UIDs and FETCHes with a
// minimal single-summary response so ListMessages can complete.
func startSearchServer(t *testing.T, searchUIDs []uint32) (addr string, criteria chan string) {
	t.Helper()
	uidLine := "* SEARCH"
	if len(searchUIDs) > 0 {
		uidLine += " " + itoaJoin(searchUIDs)
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	criteria = make(chan string, 8)
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
		uidvaliditySent := false
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
			case strings.Contains(line, "UID SEARCH"):
				criteria <- strings.TrimSpace(line)
				_, _ = io.WriteString(w, uidLine+"\r\n"+tag+" OK search done\r\n")
			case strings.Contains(line, "SELECT"):
				if !uidvaliditySent {
					_, _ = io.WriteString(w, "* OK [UIDVALIDITY 7] selected\r\n")
					uidvaliditySent = true
				}
				_, _ = io.WriteString(w, tag+" OK select done\r\n")
			case strings.Contains(line, "UID FETCH"):
				for _, u := range searchUIDs {
					_, _ = io.WriteString(w, "* 1 FETCH (UID "+strconv.FormatUint(uint64(u), 10)+" FLAGS (\\Seen) RFC822.SIZE 12 ENVELOPE (NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL))\r\n")
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
	return ln.Addr().String(), criteria
}

func TestListMessagesTotalIsFilteredSetRegardlessOfLimit(t *testing.T) {
	addr, criteria := startSearchServer(t, []uint32{3, 5, 9})
	client := dialTestClient(t, addr)
	// limit=1 mirrors the webmail unread-badge pattern: page size must not
	// shrink Total, which counts the whole filtered UID set.
	result, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Query: "is:unseen", Limit: 1})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if result.Total != 3 {
		t.Fatalf("total=%d want=3 (filtered set size, independent of limit)", result.Total)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("page=%d want=1", len(result.Messages))
	}
	select {
	case cmd := <-criteria:
		if !strings.Contains(cmd, "UNSEEN") {
			t.Fatalf("search command %q", cmd)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for UID SEARCH")
	}
}

func TestListMessagesFlaggedQuerySendsIMAPFlagged(t *testing.T) {
	addr, criteria := startSearchServer(t, []uint32{4})
	client := dialTestClient(t, addr)
	result, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Query: "is:flagged", Limit: 1})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("total=%d want=1", result.Total)
	}
	select {
	case cmd := <-criteria:
		if !strings.Contains(cmd, "FLAGGED") {
			t.Fatalf("search command %q", cmd)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for UID SEARCH")
	}
}

func TestBuildSearchCriteriaFlaggedMappedToIMAP(t *testing.T) {
	got, err := BuildSearchCriteria("is:flagged")
	if err != nil || got != "FLAGGED" {
		t.Fatalf("is:flagged -> %q err=%v", got, err)
	}
}
